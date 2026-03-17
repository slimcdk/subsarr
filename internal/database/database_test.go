package database

import (
	"testing"

	_ "github.com/slimcdk/subsarr/internal/sqlitedriver"
)

func TestOpen_SQLite(t *testing.T) {
	db, err := Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Open sqlite: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestOpen_UnsupportedDriver(t *testing.T) {
	_, err := Open("oracle", "localhost")
	if err == nil {
		t.Error("expected error for unsupported driver")
	}
}

func TestMigrate_SQLite(t *testing.T) {
	db, err := Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := Migrate(db, "sqlite"); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Verify table exists by inserting a row.
	_, err = db.Exec(`INSERT INTO subtitles (id, subscene_id, title, language, filename)
		VALUES ('test-id', 'sc-1', 'Test', 'English', 'test.srt')`)
	if err != nil {
		t.Fatalf("insert after migrate: %v", err)
	}

	// Verify indexes exist.
	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND tbl_name='subtitles'`).Scan(&count)
	if err != nil {
		t.Fatalf("count indexes: %v", err)
	}
	if count < 5 {
		t.Errorf("index count = %d, want >= 5", count)
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	db, err := Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Run migrations twice — should not error.
	if err := Migrate(db, "sqlite"); err != nil {
		t.Fatalf("Migrate 1: %v", err)
	}
	if err := Migrate(db, "sqlite"); err != nil {
		t.Fatalf("Migrate 2: %v", err)
	}
}

func TestMigrate_UnsupportedDriver(t *testing.T) {
	if err := Migrate(nil, "oracle"); err == nil {
		t.Error("expected error for unsupported driver")
	}
}

func TestMigrate_SQLite_UniqueConstraint(t *testing.T) {
	db, err := Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	Migrate(db, "sqlite")

	// Insert a record.
	_, err = db.Exec(`INSERT INTO subtitles (id, subscene_id, title, slug, language, filename)
		VALUES ('u1', 'sc-1', 'Test', 'test-slug', 'English', 'test.srt')`)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}

	// Insert duplicate (same slug + subscene_id + filename) should fail.
	_, err = db.Exec(`INSERT INTO subtitles (id, subscene_id, title, slug, language, filename)
		VALUES ('u2', 'sc-1', 'Test', 'test-slug', 'English', 'test.srt')`)
	if err == nil {
		t.Error("expected unique constraint violation")
	}
}
