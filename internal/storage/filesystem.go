package storage

import (
	"context"
	"io"
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

func (fs *Filesystem) Put(_ context.Context, key string, r io.Reader, _ int64) error {
	path := filepath.Join(fs.root, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, r)
	return err
}

func (fs *Filesystem) Get(_ context.Context, key string) (io.ReadCloser, error) {
	return os.Open(filepath.Join(fs.root, filepath.FromSlash(key)))
}

func (fs *Filesystem) Delete(_ context.Context, key string) error {
	return os.Remove(filepath.Join(fs.root, filepath.FromSlash(key)))
}
