package storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesystem_PutGetDelete(t *testing.T) {
	root := t.TempDir()
	fs := NewFilesystem(root)
	ctx := context.Background()

	content := []byte("hello subtitle world")
	key := "subtitles/abc123/test.srt"

	// Put
	if err := fs.Put(ctx, key, bytes.NewReader(content), int64(len(content))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Verify file exists on disk
	diskPath := filepath.Join(root, "subtitles", "abc123", "test.srt")
	if _, err := os.Stat(diskPath); err != nil {
		t.Fatalf("file not found on disk: %v", err)
	}

	// Get
	rc, err := fs.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("Get content = %q, want %q", got, content)
	}

	// Delete
	if err := fs.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(diskPath); !os.IsNotExist(err) {
		t.Error("file should not exist after Delete")
	}
}

func TestFilesystem_GetNotFound(t *testing.T) {
	root := t.TempDir()
	fs := NewFilesystem(root)

	_, err := fs.Get(context.Background(), "nonexistent/file.srt")
	if err == nil {
		t.Error("Get nonexistent should return error")
	}
}

func TestFilesystem_PutCreatesDirectories(t *testing.T) {
	root := t.TempDir()
	fs := NewFilesystem(root)

	key := "deep/nested/path/to/subtitle.srt"
	content := []byte("content")

	if err := fs.Put(context.Background(), key, bytes.NewReader(content), int64(len(content))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	diskPath := filepath.Join(root, "deep", "nested", "path", "to", "subtitle.srt")
	if _, err := os.Stat(diskPath); err != nil {
		t.Fatalf("deeply nested file not created: %v", err)
	}
}

func TestFilesystem_PutOverwrites(t *testing.T) {
	root := t.TempDir()
	fs := NewFilesystem(root)
	ctx := context.Background()
	key := "overwrite/test.srt"

	// Write initial content
	fs.Put(ctx, key, bytes.NewReader([]byte("first")), 5)

	// Overwrite
	fs.Put(ctx, key, bytes.NewReader([]byte("second")), 6)

	rc, _ := fs.Get(ctx, key)
	got, _ := io.ReadAll(rc)
	rc.Close()

	if string(got) != "second" {
		t.Errorf("overwritten content = %q, want %q", got, "second")
	}
}
