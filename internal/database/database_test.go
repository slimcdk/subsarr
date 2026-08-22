package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	// A file, not :memory:, because the pool would otherwise hand each
	// connection its own empty database.
	db, err := Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func migrate(t *testing.T, db *sql.DB) *Migrator {
	t.Helper()
	m, err := NewMigrator(db, "sqlite")
	if err != nil {
		t.Fatalf("NewMigrator: %v", err)
	}
	if err := m.Up(context.Background()); err != nil {
		t.Fatalf("Up: %v", err)
	}
	return m
}

func TestOpen_UnsupportedDriver(t *testing.T) {
	if _, err := Open("oracle", "localhost"); err == nil {
		t.Error("expected an error for an unsupported driver")
	}
}

// Without parseTime the MySQL driver returns the migration table's timestamps as
// raw bytes and every migration command fails on a healthy database.
func TestOpen_MySQLDSNForcesParseTime(t *testing.T) {
	tests := []string{
		"user:pass@tcp(localhost:3306)/subsarr",
		"user:pass@tcp(localhost:3306)/subsarr?charset=utf8mb4",
		"user:pass@tcp(localhost:3306)/subsarr?parseTime=false",
	}
	for _, dsn := range tests {
		got, err := mysqlDSN(dsn)
		if err != nil {
			t.Fatalf("mysqlDSN(%q): %v", dsn, err)
		}
		if !strings.Contains(got, "parseTime=true") {
			t.Errorf("mysqlDSN(%q) = %q, want parseTime=true", dsn, got)
		}
	}
}

func TestMigrate_FreshDatabase(t *testing.T) {
	db := openTestDB(t)
	migrate(t, db)

	for _, table := range []string{"uploads", "files", "titles"} {
		exists, err := tableExists(context.Background(), db, "sqlite", table)
		if err != nil || !exists {
			t.Errorf("table %s: exists=%v err=%v", table, exists, err)
		}
	}

	// The flat table is created by version 1 and dropped again by version 4, so
	// a fresh installation ends up with only the catalogue model.
	exists, _ := tableExists(context.Background(), db, "sqlite", "subtitles")
	if exists {
		t.Error("the legacy subtitles table should be gone after a fresh migration")
	}
}

func TestMigrate_IsIdempotent(t *testing.T) {
	db := openTestDB(t)
	migrate(t, db)

	m, err := NewMigrator(db, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Up(context.Background()); err != nil {
		t.Fatalf("second Up: %v", err)
	}
}

func TestMigrate_Status(t *testing.T) {
	db := openTestDB(t)
	m := migrate(t, db)

	lines, err := m.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(lines) != 4 {
		t.Fatalf("got %d migrations, want 4:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	for _, line := range lines {
		if strings.Contains(line, "pending") {
			t.Errorf("migration still pending after Up: %s", line)
		}
	}
	if !strings.Contains(strings.Join(lines, "\n"), "legacy data migration") {
		t.Error("the Go migration should be listed by name")
	}
}

// The upgrade an existing installation goes through: a flat `subtitles` table
// with no migration table at all.
func seedLegacy(t *testing.T, db *sql.DB) {
	t.Helper()

	const schema = `
	CREATE TABLE subtitles (
		id           TEXT PRIMARY KEY,
		subscene_id  TEXT NOT NULL,
		title        TEXT NOT NULL,
		slug         TEXT NOT NULL DEFAULT '',
		imdb_id      TEXT NOT NULL DEFAULT '',
		language     TEXT NOT NULL,
		hi           INTEGER NOT NULL DEFAULT 0,
		author       TEXT NOT NULL DEFAULT '',
		releases     TEXT NOT NULL DEFAULT '[]',
		comment      TEXT NOT NULL DEFAULT '',
		year         INTEGER NOT NULL DEFAULT 0,
		filename     TEXT NOT NULL,
		format       TEXT NOT NULL DEFAULT '',
		content_key  TEXT NOT NULL DEFAULT '',
		content_hash TEXT NOT NULL DEFAULT '',
		uploaded_at  TEXT NOT NULL DEFAULT '',
		downloads    INTEGER NOT NULL DEFAULT 0,
		created_at   TEXT NOT NULL DEFAULT '',
		updated_at   TEXT NOT NULL DEFAULT ''
	);
	INSERT INTO subtitles (id, subscene_id, title, slug, imdb_id, language, hi, filename, format, content_key, content_hash, downloads)
	VALUES
		('file-1', '1001', 'The Dark Knight', 'the-dark-knight', '', '2_english', 0, 'tdk.srt', 'srt', 'subtitles/file-1/tdk.srt', 'aaaa', 7),
		('file-2', '1002', 'The Lion King', 'the-lion-king-1994', '', 'brazillian-portuguese', 1, 'tlk.srt', 'srt', 'subtitles/file-2/tlk.srt', 'bbbb', 0),
		('file-3', '1003', 'Placeholder', 'placeholder', '', 'english', 0, 'p.zip', 'zip', '', '', 0);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("seed legacy schema: %v", err)
	}
}

func TestMigrate_BaselinesAndMigratesAnExistingInstallation(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	seedLegacy(t, db)

	migrate(t, db)

	var uploads, files int
	if err := db.QueryRow("SELECT COUNT(*) FROM uploads").Scan(&uploads); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM files").Scan(&files); err != nil {
		t.Fatal(err)
	}
	if uploads != 3 {
		t.Errorf("uploads = %d, want 3 — every catalogue row is kept", uploads)
	}
	if files != 2 {
		t.Errorf("files = %d, want 2 — the placeholder row had no stored content", files)
	}

	// The id is the download URL an operator's Bazarr already holds.
	var uploadID string
	if err := db.QueryRow("SELECT upload_id FROM files WHERE id = 'file-1'").Scan(&uploadID); err != nil {
		t.Fatalf("the legacy file id was not preserved: %v", err)
	}
	if uploadID != "1001" {
		t.Errorf("upload_id = %q, want the Subscene id", uploadID)
	}

	// The polluted language column is the reason 3 % of rows were unreachable.
	var language string
	if err := db.QueryRow("SELECT language FROM uploads WHERE id = '1001'").Scan(&language); err != nil {
		t.Fatal(err)
	}
	if language != "english" {
		t.Errorf("language = %q, want it canonicalised to english", language)
	}

	var year int
	if err := db.QueryRow("SELECT year FROM uploads WHERE id = '1002'").Scan(&year); err != nil {
		t.Fatal(err)
	}
	if year != 1994 {
		t.Errorf("year = %d, want the year recovered from the slug", year)
	}

	var hi bool
	if err := db.QueryRow("SELECT hi FROM uploads WHERE id = '1002'").Scan(&hi); err != nil {
		t.Fatal(err)
	}
	if !hi {
		t.Error("the hearing-impaired flag was lost")
	}

	var downloads int
	if err := db.QueryRow("SELECT downloads FROM files WHERE id = 'file-1'").Scan(&downloads); err != nil {
		t.Fatal(err)
	}
	if downloads != 7 {
		t.Errorf("downloads = %d, want the counter carried over", downloads)
	}

	if exists, _ := tableExists(ctx, db, "sqlite", "subtitles"); exists {
		t.Error("the legacy table should have been dropped")
	}
}

// A migration that fails partway must be safe to re-run, which means running the
// data migration twice must not duplicate anything.
func TestMigrate_LegacyDataMigrationIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	seedLegacy(t, db)
	migrate(t, db)

	// Re-run the data migration directly: after version 4 the legacy table is
	// gone, so it should do nothing at all.
	if err := migrateLegacyRows(ctx, db, "sqlite"); err != nil {
		t.Fatalf("re-run: %v", err)
	}

	var files int
	if err := db.QueryRow("SELECT COUNT(*) FROM files").Scan(&files); err != nil {
		t.Fatal(err)
	}
	if files != 2 {
		t.Errorf("files = %d, want 2", files)
	}
}

// An older installation upgraded before content hashes existed has fewer columns
// than the migration expects.
func TestMigrate_LegacyTableWithMissingColumns(t *testing.T) {
	db := openTestDB(t)

	const schema = `
	CREATE TABLE subtitles (
		id          TEXT PRIMARY KEY,
		subscene_id TEXT NOT NULL,
		title       TEXT NOT NULL,
		slug        TEXT NOT NULL DEFAULT '',
		language    TEXT NOT NULL,
		filename    TEXT NOT NULL
	);
	INSERT INTO subtitles (id, subscene_id, title, slug, language, filename)
	VALUES ('file-1', '1001', 'The Dark Knight', 'the-dark-knight', 'english', 'tdk.srt');
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}

	migrate(t, db)

	var uploads int
	if err := db.QueryRow("SELECT COUNT(*) FROM uploads").Scan(&uploads); err != nil {
		t.Fatal(err)
	}
	if uploads != 1 {
		t.Errorf("uploads = %d, want 1", uploads)
	}
}
