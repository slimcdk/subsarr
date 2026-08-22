package storage

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Filesystem stores files on the local filesystem.
type Filesystem struct {
	root string
}

func NewFilesystem(root string) *Filesystem {
	return &Filesystem{root: root}
}

// Put writes to a temporary file and renames it into place. An import that is
// interrupted mid-write must not leave a half-written subtitle behind that looks,
// to the next run, like a file it has already stored.
func (fsys *Filesystem) Put(_ context.Context, key string, r io.Reader, _ int64) error {
	path := fsys.path(key)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".subsarr-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (fsys *Filesystem) Get(_ context.Context, key string) (io.ReadCloser, error) {
	return os.Open(fsys.path(key))
}

func (fsys *Filesystem) Exists(_ context.Context, key string) (bool, error) {
	_, err := os.Stat(fsys.path(key))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// Delete removes an object. An object that is already gone is not an error: the
// caller wanted it absent, and it is.
func (fsys *Filesystem) Delete(_ context.Context, key string) error {
	err := os.Remove(fsys.path(key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (fsys *Filesystem) path(key string) string {
	return filepath.Join(fsys.root, filepath.FromSlash(key))
}
