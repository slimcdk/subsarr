// Package archive reads the Subscene dump.
//
// The dump is a 97 GB split 7z that is solid-compressed in 27 blocks: reading it
// in archive order is cheap, seeking around it is not. Everything here is
// therefore an in-order stream over entries, never random access — and nothing
// is ever extracted to disk.
package archive

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/bodgit/sevenzip"
)

// Entry is one file inside a source.
type Entry interface {
	// Name is the entry's path within the source, always slash-separated.
	Name() string
	Size() int64
	Open() (io.ReadCloser, error)
}

// Source yields entries in the order they are stored.
//
// Each stops and returns the error if fn returns one, so a caller can abort a
// long run — and returns ErrStop as nil, so it can also stop on purpose.
type Source interface {
	Each(fn func(Entry) error) error
	Close() error
}

// ErrStop ends iteration without failing it.
var ErrStop = errors.New("stop iteration")

// ─── 7z ──────────────────────────────────────────────────────────────────────

type sevenZipSource struct {
	reader *sevenzip.ReadCloser
}

// OpenSevenZip opens a (possibly split) 7z archive for streaming. Pass the first
// volume; the rest are found next to it.
func OpenSevenZip(path string) (Source, error) {
	r, err := sevenzip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	return &sevenZipSource{reader: r}, nil
}

func (s *sevenZipSource) Each(fn func(Entry) error) error {
	for _, f := range s.reader.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if err := fn(sevenZipEntry{f}); err != nil {
			if errors.Is(err, ErrStop) {
				return nil
			}
			return err
		}
	}
	return nil
}

func (s *sevenZipSource) Close() error { return s.reader.Close() }

type sevenZipEntry struct{ f *sevenzip.File }

func (e sevenZipEntry) Name() string { return normalisePath(e.f.Name) }
func (e sevenZipEntry) Size() int64  { return int64(e.f.UncompressedSize) }
func (e sevenZipEntry) Open() (io.ReadCloser, error) {
	return e.f.Open()
}

// ─── directory ───────────────────────────────────────────────────────────────

type dirSource struct{ root string }

// OpenDir treats an already-extracted directory tree as a source, which is what
// an operator who unpacked the archive themselves has.
func OpenDir(root string) (Source, error) {
	if _, err := os.Stat(root); err != nil {
		return nil, err
	}
	return &dirSource{root: root}, nil
}

func (s *dirSource) Each(fn func(Entry) error) error {
	err := filepath.WalkDir(s.root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(s.root, path)
		if err != nil {
			return err
		}
		if err := fn(dirEntry{path: path, name: normalisePath(rel), size: info.Size()}); err != nil {
			if errors.Is(err, ErrStop) {
				return fs.SkipAll
			}
			return err
		}
		return nil
	})
	return err
}

func (s *dirSource) Close() error { return nil }

type dirEntry struct {
	path string
	name string
	size int64
}

func (e dirEntry) Name() string                 { return e.name }
func (e dirEntry) Size() int64                  { return e.size }
func (e dirEntry) Open() (io.ReadCloser, error) { return os.Open(e.path) }

// ─── in-memory ───────────────────────────────────────────────────────────────

// MemoryEntry is an entry held in memory. Tests use it to build an archive
// without writing one: a 7z cannot be created from Go, so this is the seam the
// importer is tested at.
type MemoryEntry struct {
	Path    string
	Content []byte
}

func (e MemoryEntry) Name() string { return normalisePath(e.Path) }
func (e MemoryEntry) Size() int64  { return int64(len(e.Content)) }
func (e MemoryEntry) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(e.Content)), nil
}

// MemorySource is a Source over a slice of entries.
type MemorySource []MemoryEntry

func (s MemorySource) Each(fn func(Entry) error) error {
	for _, e := range s {
		if err := fn(e); err != nil {
			if errors.Is(err, ErrStop) {
				return nil
			}
			return err
		}
	}
	return nil
}

func (s MemorySource) Close() error { return nil }

// normalisePath makes an entry name comparable with a catalogue path: the dump
// was built on Windows and its paths come back with either separator.
func normalisePath(name string) string {
	name = strings.ReplaceAll(name, `\`, "/")
	return strings.TrimPrefix(name, "./")
}
