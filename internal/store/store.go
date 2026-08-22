// Package store is subsarr's data access layer.
//
// The catalogue is normalised into two tables: `uploads` holds one row per
// Subscene upload — the metadata Subscene itself showed — and `files` holds one
// row per downloadable subtitle file extracted from that upload's archive entry.
// A search joins the two; a download reads one `files` row.
//
// There is a single SQL implementation (sqlStore) parameterised by a dialect,
// rather than one implementation per database. Search semantics are the part of
// this service that must not differ between SQLite, PostgreSQL and MySQL, and the
// cheapest way to guarantee that is to have only one copy of them.
package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Upload is one Subscene upload: the catalogue metadata, stored once.
type Upload struct {
	ID         string // the Subscene upload id, or a derived id when there is none
	SubsceneID string
	FilePath   string // the entry's path inside the archive
	Slug       string
	Title      string
	ImdbID     string // "tt" + at least 7 digits, empty when unknown
	Language   string // canonical (see internal/lang)
	HI         bool
	Year       int // 0 = unknown
	Author     string
	AuthorID   string
	Comment    string
	Releases   string // JSON array, "[]" when unknown
	UploadedAt string // RFC 3339, empty when unknown
}

// File is one downloadable subtitle file.
type File struct {
	ID          string // the id Bazarr downloads by; preserved across re-imports
	UploadID    string
	Filename    string
	Format      string
	ContentHash string // SHA-256 of the file's bytes
	ContentKey  string // storage key, content-addressed
	Size        int64
	Downloads   int
}

// Subtitle is what a search returns: a file together with the upload it came
// from. It is flat because that is how it is served.
type Subtitle struct {
	ID          string
	UploadID    string
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

type LanguageCount struct {
	Language string
	Count    int
}

// SearchParams is one search. Empty fields are not filtered on.
type SearchParams struct {
	ImdbID   string
	Language string
	Slug     string
	Query    string
	HI       *bool // nil = both
	Year     int   // 0 = any
	SeasonEp string
	Limit    int
	Offset   int
}

// IngestFile is one subtitle file offered to the store. The caller has already
// hashed the content and written it to storage.
type IngestFile struct {
	ID          string // reused when this (upload, content) is already known
	UploadID    string
	Filename    string
	Format      string
	ContentHash string
	ContentKey  string
	Size        int64
}

// PruneBatch is one pass of PruneLanguages.
type PruneBatch struct {
	Deleted    int      // `files` rows removed
	Bytes      int64    // their combined size
	OrphanKeys []string // storage keys no remaining row references
}

// Store is the whole database surface. Search and ingest live behind the same
// interface so that the conformance suite can prove every dialect agrees.
type Store interface {
	// Reading — the API's surface.
	GetSubtitle(ctx context.Context, id string) (*Subtitle, error)
	SearchSubtitles(ctx context.Context, p SearchParams) ([]Subtitle, int, error)
	ListLanguages(ctx context.Context) ([]LanguageCount, error)
	HasIMDBIDs(ctx context.Context) (bool, error)
	IncrementDownloads(ctx context.Context, id string) error

	// Importing.
	CountUploads(ctx context.Context) (int64, error)
	UpsertUploads(ctx context.Context, uploads []Upload) error
	UploadsByPath(ctx context.Context, paths []string) (map[string]Upload, error)
	UploadsByID(ctx context.Context, ids []string) (map[string]Upload, error)
	FilesByUpload(ctx context.Context, uploadIDs []string) (map[string][]File, error)
	IngestFiles(ctx context.Context, files []IngestFile) error
	CatalogueLanguages(ctx context.Context) ([]string, error)

	// Maintenance.
	PruneScope(ctx context.Context, keep []string) (rows int64, bytes int64, err error)
	PruneLanguages(ctx context.Context, keep []string, batch int) (PruneBatch, error)
	Reindex(ctx context.Context) error
	Optimize(ctx context.Context) error
	Checkpoint(ctx context.Context) error

	// DB exposes the connection for the migrator and for tests.
	DB() *sql.DB
}

// New creates a Store for the given database driver.
func New(db *sql.DB, driver string) (Store, error) {
	d, err := newDialect(driver)
	if err != nil {
		return nil, err
	}
	return &sqlStore{db: db, d: d}, nil
}

func newDialect(driver string) (dialect, error) {
	switch driver {
	case "sqlite":
		return sqliteDialect{}, nil
	case "postgres":
		return postgresDialect{}, nil
	case "mysql":
		return mysqlDialect{}, nil
	default:
		return nil, fmt.Errorf("unsupported database driver: %s", driver)
	}
}
