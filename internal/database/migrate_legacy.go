package database

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/pressly/goose/v3"
	"github.com/slimcdk/subsarr/internal/lang"
	"github.com/slimcdk/subsarr/internal/title"
)

// legacyBatch is how many legacy rows are copied per transaction.
const legacyBatch = 1000

// legacyDataMigration is schema version 3: it copies the flat `subtitles` table
// into `uploads` + `files`.
//
// It is Go rather than SQL because the languages have to be canonicalised on the
// way across — that is the whole point of the migration for an existing
// installation — and because the legacy table's exact columns differ between
// installations that upgraded at different times.
//
// Every file keeps its id, so a Bazarr history or an in-flight download still
// resolves after the upgrade. Rows with no stored content produce no `files` row:
// those are the placeholders that answered a download with a 404.
func legacyDataMigration(driver string) *goose.Migration {
	return goose.NewGoMigration(3,
		&goose.GoFunc{
			// Outside a transaction: this touches every row of a table that can
			// hold five million of them, and it is safe to re-run.
			RunDB: func(ctx context.Context, db *sql.DB) error {
				return migrateLegacyRows(ctx, db, driver)
			},
		},
		&goose.GoFunc{
			RunDB: func(context.Context, *sql.DB) error { return nil },
		},
	)
}

// legacyRow is one row of the flat table.
type legacyRow struct {
	ID          string
	SubsceneID  string
	Title       string
	Slug        string
	ImdbID      string
	Language    string
	HI          bool
	Author      string
	Releases    string
	Comment     string
	Year        int
	Filename    string
	Format      string
	ContentKey  string
	ContentHash string
	UploadedAt  string
	Downloads   int
}

func migrateLegacyRows(ctx context.Context, db *sql.DB, driver string) error {
	exists, err := tableExists(ctx, db, driver, "subtitles")
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}

	selectList, err := legacySelectList(ctx, db, driver)
	if err != nil {
		return err
	}

	var (
		lastID   string
		rows     int // legacy rows read; several can belong to one upload
		files    int
		orphaned int
	)
	for {
		batch, err := readLegacyPage(ctx, db, driver, selectList, lastID)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		lastID = batch[len(batch)-1].ID

		usable := batch[:0]
		for _, r := range batch {
			if r.SubsceneID == "" {
				// Without a Subscene id there is no upload to attach the file to.
				orphaned++
				continue
			}
			usable = append(usable, r)
		}

		u, f, err := writeLegacyBatch(ctx, db, driver, usable)
		if err != nil {
			return err
		}
		rows += u
		files += f

		if rows%(legacyBatch*50) < legacyBatch {
			log.Printf("[migrate] legacy data: %d rows, %d files", rows, files)
			checkpoint(ctx, db, driver)
		}
	}

	if rows == 0 && orphaned == 0 {
		return nil
	}

	// The row counts are what the tables ended up holding, not what was read:
	// several legacy rows can belong to one upload.
	var uploadCount, fileCount int64
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM uploads").Scan(&uploadCount)
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM files").Scan(&fileCount)
	log.Printf("[migrate] legacy data migrated: %d rows read → %d uploads, %d files (%d rows had no Subscene id)",
		rows, uploadCount, fileCount, orphaned)
	return nil
}

// readLegacyPage reads one page of the flat table, ordered by its primary key.
//
// Paging rather than streaming one long query is what keeps this migration from
// needing as much free disk as the database itself: an open read holds a
// snapshot, and while it is held SQLite cannot check its write-ahead log back
// into the database file, so the log grows by every row the migration writes.
func readLegacyPage(ctx context.Context, db *sql.DB, driver, selectList, after string) ([]legacyRow, error) {
	query := fmt.Sprintf("SELECT %s FROM subtitles WHERE id > %s ORDER BY id LIMIT %d",
		selectList, placeholders(driver, 1), legacyBatch)

	rows, err := db.QueryContext(ctx, query, after)
	if err != nil {
		return nil, fmt.Errorf("read legacy subtitles: %w", err)
	}
	defer rows.Close()

	page := make([]legacyRow, 0, legacyBatch)
	for rows.Next() {
		var r legacyRow
		if err := rows.Scan(&r.ID, &r.SubsceneID, &r.Title, &r.Slug, &r.ImdbID, &r.Language,
			&r.HI, &r.Author, &r.Releases, &r.Comment, &r.Year, &r.Filename, &r.Format,
			&r.ContentKey, &r.ContentHash, &r.UploadedAt, &r.Downloads); err != nil {
			return nil, fmt.Errorf("scan legacy row: %w", err)
		}
		page = append(page, r)
	}
	return page, rows.Err()
}

// checkpoint folds SQLite's write-ahead log back into the database file.
//
// SQLite checkpoints on its own, but only opportunistically and never while
// another connection is reading. Copying five million rows with random primary
// keys, reading between every batch, defeats that: the log grew to eighteen
// gigabytes on the reference database before this was added — more free disk
// than the database itself needs.
func checkpoint(ctx context.Context, db *sql.DB, driver string) {
	if driver != "sqlite" {
		return
	}
	var busy, size, checkpointed int
	if err := db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &size, &checkpointed); err != nil {
		log.Printf("[migrate] checkpoint failed: %v", err)
		return
	}
	if busy != 0 {
		log.Printf("[migrate] checkpoint blocked by a reader, %d pages still in the log", size)
	}
}

// legacyColumns is what the migration reads, in scan order, with the fallback to
// use when an older installation does not have the column at all.
var legacyColumns = []struct{ name, fallback string }{
	{"id", "''"},
	{"subscene_id", "''"},
	{"title", "''"},
	{"slug", "''"},
	{"imdb_id", "''"},
	{"language", "''"},
	{"hi", "0"},
	{"author", "''"},
	{"releases", "'[]'"},
	{"comment", "''"},
	{"year", "0"},
	{"filename", "''"},
	{"format", "''"},
	{"content_key", "''"},
	{"content_hash", "''"},
	{"uploaded_at", "''"},
	{"downloads", "0"},
}

// legacySelectList builds the projection from the columns the table actually
// has. Installations that upgraded at different times have different columns,
// and a migration that assumes the newest one fails on the oldest database.
func legacySelectList(ctx context.Context, db *sql.DB, driver string) (string, error) {
	rows, err := db.QueryContext(ctx, "SELECT * FROM subtitles WHERE 1 = 0")
	if err != nil {
		return "", fmt.Errorf("inspect legacy subtitles: %w", err)
	}
	defer rows.Close()

	names, err := rows.Columns()
	if err != nil {
		return "", err
	}
	present := make(map[string]struct{}, len(names))
	for _, n := range names {
		present[strings.ToLower(n)] = struct{}{}
	}

	parts := make([]string, 0, len(legacyColumns))
	for _, col := range legacyColumns {
		if _, ok := present[col.name]; !ok {
			parts = append(parts, col.fallback+" AS "+col.name)
			continue
		}
		if col.name == "uploaded_at" {
			parts = append(parts, legacyUploadedAt(driver))
			continue
		}
		parts = append(parts, col.name)
	}
	return strings.Join(parts, ", "), nil
}

// legacyUploadedAt renders the timestamp as the RFC 3339 string the new schema
// stores, in the dialect's own way, so the scan does not have to know the column
// was a timestamp on two of the three databases.
func legacyUploadedAt(driver string) string {
	switch driver {
	case "postgres":
		return `COALESCE(to_char(uploaded_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS uploaded_at`
	case "mysql":
		return `COALESCE(DATE_FORMAT(uploaded_at, '%Y-%m-%dT%H:%i:%sZ'), '') AS uploaded_at`
	default:
		return "uploaded_at"
	}
}

func writeLegacyBatch(ctx context.Context, db *sql.DB, driver string, batch []legacyRow) (rows, files int, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	uploadStmt, err := tx.PrepareContext(ctx, legacyUploadInsert(driver))
	if err != nil {
		return 0, 0, err
	}
	defer uploadStmt.Close()

	fileStmt, err := tx.PrepareContext(ctx, legacyFileInsert(driver))
	if err != nil {
		return 0, 0, err
	}
	defer fileStmt.Close()

	for _, r := range batch {
		releases := r.Releases
		if strings.TrimSpace(releases) == "" {
			releases = "[]"
		}
		year := r.Year
		if year == 0 {
			year = title.YearFromSlug(r.Slug)
		}
		titleText := r.Title
		if strings.TrimSpace(titleText) == "" {
			titleText = title.FromSlug(r.Slug)
		}

		if _, err := uploadStmt.ExecContext(ctx,
			r.SubsceneID, r.SubsceneID, "", r.Slug, titleText, r.ImdbID,
			lang.Canonical(r.Language), r.HI, year, r.Author, "", r.Comment, releases, r.UploadedAt,
		); err != nil {
			return 0, 0, fmt.Errorf("migrate upload %s: %w", r.SubsceneID, err)
		}
		rows++

		// A row with no stored content is a catalogue entry, not a file: keeping
		// it would keep answering downloads with a 404.
		if r.ContentKey == "" || r.ContentHash == "" {
			continue
		}
		if _, err := fileStmt.ExecContext(ctx,
			r.ID, r.SubsceneID, r.Filename, r.Format, r.ContentHash, r.ContentKey, 0, r.Downloads,
		); err != nil {
			return 0, 0, fmt.Errorf("migrate file %s: %w", r.ID, err)
		}
		files++
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return rows, files, nil
}

// The three helpers below duplicate what internal/store's dialects do. That is
// deliberate: a data migration is pinned to the schema of the moment it was
// written, and one that followed the store's code as the store evolves would
// stop reproducing the migration it is supposed to be.

func legacyUploadInsert(driver string) string {
	cols := []string{"id", "subscene_id", "file_path", "slug", "title", "imdb_id", "language",
		"hi", "year", "author", "author_id", "comment", "releases", "uploaded_at"}
	return "INSERT INTO uploads (" + strings.Join(cols, ", ") + ") VALUES (" +
		placeholders(driver, len(cols)) + ")" + onConflictUpdate(driver, []string{"id"}, cols[1:])
}

func legacyFileInsert(driver string) string {
	cols := []string{"id", "upload_id", "filename", "format", "content_hash", "content_key", "size", "downloads"}
	return "INSERT INTO files (" + strings.Join(cols, ", ") + ") VALUES (" +
		placeholders(driver, len(cols)) + ")" +
		onConflictUpdate(driver, []string{"upload_id", "content_hash"}, []string{"filename", "format", "content_key"})
}

func placeholders(driver string, n int) string {
	parts := make([]string, n)
	for i := range parts {
		if driver == "postgres" {
			parts[i] = "$" + strconv.Itoa(i+1)
		} else {
			parts[i] = "?"
		}
	}
	return strings.Join(parts, ", ")
}

func onConflictUpdate(driver string, conflict, update []string) string {
	if driver == "mysql" {
		sets := make([]string, len(update))
		for i, col := range update {
			sets[i] = col + " = VALUES(" + col + ")"
		}
		return " ON DUPLICATE KEY UPDATE " + strings.Join(sets, ", ")
	}
	sets := make([]string, len(update))
	for i, col := range update {
		sets[i] = col + " = excluded." + col
	}
	return " ON CONFLICT (" + strings.Join(conflict, ", ") + ") DO UPDATE SET " + strings.Join(sets, ", ")
}
