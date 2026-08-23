package archive

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestMemorySource(t *testing.T) {
	src := MemorySource{
		{Path: "a/one.zip", Content: []byte("first")},
		{Path: `b\two.zip`, Content: []byte("second")},
	}

	var names []string
	err := src.Each(func(e Entry) error {
		names = append(names, e.Name())

		rc, err := e.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		content, err := io.ReadAll(rc)
		if err != nil {
			return err
		}
		if int64(len(content)) != e.Size() {
			t.Errorf("%s: size = %d, content = %d bytes", e.Name(), e.Size(), len(content))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Each: %v", err)
	}

	// The dump was built on Windows; its paths come back with either separator
	// and must be comparable with the catalogue's.
	if names[1] != "b/two.zip" {
		t.Errorf("name = %q, want the separator normalised", names[1])
	}
}

// ErrStop is how the catalogue pass leaves the stream as soon as it has what it
// came for, instead of decompressing the rest of the archive.
func TestEach_ErrStopEndsIterationWithoutFailing(t *testing.T) {
	src := MemorySource{
		{Path: "one"}, {Path: "two"}, {Path: "three"},
	}

	seen := 0
	err := src.Each(func(Entry) error {
		seen++
		return ErrStop
	})
	if err != nil {
		t.Fatalf("Each returned %v, want nil", err)
	}
	if seen != 1 {
		t.Errorf("visited %d entries, want 1", seen)
	}
}

func TestDirSource(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "the-dark-knight"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "the-dark-knight", "a.srt")
	if err := os.WriteFile(path, []byte("subtitle"), 0o644); err != nil {
		t.Fatal(err)
	}

	src, err := OpenDir(root)
	if err != nil {
		t.Fatalf("OpenDir: %v", err)
	}
	defer src.Close()

	var entries []Entry
	if err := src.Each(func(e Entry) error {
		entries = append(entries, e)
		return nil
	}); err != nil {
		t.Fatalf("Each: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Name() != "the-dark-knight/a.srt" {
		t.Errorf("name = %q, want a path relative to the root", entries[0].Name())
	}
	if entries[0].Size() != 8 {
		t.Errorf("size = %d, want 8", entries[0].Size())
	}
}

func TestOpenDir_Missing(t *testing.T) {
	if _, err := OpenDir(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("expected an error for a directory that is not there")
	}
}

func TestOpenSevenZip_NotAnArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-an-archive.7z")
	if err := os.WriteFile(path, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSevenZip(path); err == nil {
		t.Error("expected an error for a file that is not a 7z archive")
	}
}
