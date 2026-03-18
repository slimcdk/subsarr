package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/slimcdk/subsarr/internal/database/mysqldb"
	"github.com/slimcdk/subsarr/internal/database/pgdb"
	"github.com/slimcdk/subsarr/internal/database/sqlitedb"
)

// Subtitle is the common model shared across all database dialects.
type Subtitle struct {
	ID         string
	SubsceneID string
	Title      string
	Slug       string
	ImdbID     string
	Language   string
	HI         bool
	Author     string
	Releases   string // JSON array as string
	Comment    string
	Year       int
	Filename   string
	Format     string
	ContentKey  string
	ContentHash string
	UploadedAt  string
	Downloads  int
}

type LanguageCount struct {
	Language string
	Count    int
}

type SearchParams struct {
	ImdbID   string
	Language string
	Slug     string
	HI       *bool
	Year     int
	Query    string
	SeasonEp string
	Limit    int
	Offset   int
}

type Store interface {
	GetSubtitle(ctx context.Context, id string) (*Subtitle, error)
	InsertSubtitle(ctx context.Context, s *Subtitle) (bool, error)
	InsertSubtitleBatch(ctx context.Context, subs []*Subtitle) (inserted, skipped, errors int)
	IncrementDownloads(ctx context.Context, id string) error
	ListLanguages(ctx context.Context) ([]LanguageCount, error)
	SearchSubtitles(ctx context.Context, p SearchParams) ([]Subtitle, int, error)
}

// New creates a Store for the given database driver.
func New(db *sql.DB, driver string) (Store, error) {
	switch driver {
	case "sqlite":
		return &sqliteStore{q: sqlitedb.New(db), db: db}, nil
	case "postgres":
		return &pgStore{q: pgdb.New(db), db: db}, nil
	case "mysql":
		return &mysqlStore{q: mysqldb.New(db), db: db}, nil
	default:
		return nil, fmt.Errorf("unsupported driver: %s", driver)
	}
}
