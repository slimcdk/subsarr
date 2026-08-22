// Package database opens the configured database and keeps its schema current.
//
// Migrations are versioned (goose) and embedded in the binary, one numbered set
// per dialect, so upgrading the container is a pull and a restart. Version 1 is
// the flat schema every installation before the catalogue model ran: a fresh
// database creates it empty, an existing one is baselined at it, and the two
// paths converge from there.
package database

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"strings"

	"github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	_ "github.com/slimcdk/subsarr/internal/sqlitedriver"
)

//go:embed migrations
var migrationsFS embed.FS

// Open returns a database connection for the given driver and DSN.
func Open(driver, dsn string) (*sql.DB, error) {
	var driverName string
	switch driver {
	case "sqlite":
		driverName = "sqlite3_subsarr"
	case "postgres":
		driverName = "pgx"
	case "mysql":
		driverName = "mysql"
		var err error
		if dsn, err = mysqlDSN(dsn); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported database driver: %s", driver)
	}

	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// mysqlDSN forces parseTime on. Without it the driver hands back the migration
// table's timestamps as raw bytes and every migration command fails on a
// perfectly healthy database — a footgun that has cost operators an evening.
func mysqlDSN(dsn string) (string, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return "", fmt.Errorf("parse MySQL DSN: %w", err)
	}
	cfg.ParseTime = true
	return cfg.FormatDSN(), nil
}

// Migrator applies and reports the schema version.
type Migrator struct {
	db       *sql.DB
	driver   string
	provider *goose.Provider
}

func NewMigrator(db *sql.DB, driver string) (*Migrator, error) {
	dialect, err := gooseDialect(driver)
	if err != nil {
		return nil, err
	}
	sub, err := fs.Sub(migrationsFS, "migrations/"+driver)
	if err != nil {
		return nil, err
	}

	opts := []goose.ProviderOption{
		// Version 3 canonicalises languages while it copies, which needs the
		// same Go code the importer and the API use.
		goose.WithGoMigrations(legacyDataMigration(driver)),
	}
	if driver == "postgres" {
		// PostgreSQL is the one dialect that can hold a real advisory lock, so
		// two containers starting at once cannot both migrate.
		locker, err := newPostgresLocker()
		if err != nil {
			return nil, err
		}
		opts = append(opts, goose.WithSessionLocker(locker))
	}

	provider, err := goose.NewProvider(dialect, db, sub, opts...)
	if err != nil {
		return nil, err
	}
	return &Migrator{db: db, driver: driver, provider: provider}, nil
}

func gooseDialect(driver string) (goose.Dialect, error) {
	switch driver {
	case "sqlite":
		return goose.DialectSQLite3, nil
	case "postgres":
		return goose.DialectPostgres, nil
	case "mysql":
		return goose.DialectMySQL, nil
	default:
		return "", fmt.Errorf("unsupported driver: %s", driver)
	}
}

// Up brings the database to the newest schema version, baselining first if this
// is an installation that predates versioned migrations.
func (m *Migrator) Up(ctx context.Context) error {
	if err := m.baseline(ctx); err != nil {
		return err
	}

	results, err := m.provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if len(results) > 0 {
		log.Printf("[migrate] applied %d migration(s), schema is now at version %d",
			len(results), results[len(results)-1].Source.Version)
	}
	return nil
}

// baseline records version 1 for a database that already carries the flat schema
// but has never seen a migration table. The migration itself is idempotent, so
// applying it is the record: nothing is created and nothing is lost.
func (m *Migrator) baseline(ctx context.Context) error {
	version, err := m.provider.GetDBVersion(ctx)
	if err == nil && version > 0 {
		return nil
	}

	legacy, err := m.tableExists(ctx, "subtitles")
	if err != nil {
		return err
	}
	if !legacy {
		return nil
	}

	if _, err := m.provider.ApplyVersion(ctx, 1, true); err != nil {
		return fmt.Errorf("baseline existing database: %w", err)
	}
	log.Print("[migrate] existing installation baselined at schema version 1")
	return nil
}

// Status returns one line per migration, applied or not.
func (m *Migrator) Status(ctx context.Context) ([]string, error) {
	statuses, err := m.provider.Status(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(statuses))
	for _, s := range statuses {
		state := "pending"
		if s.State == goose.StateApplied {
			state = s.AppliedAt.UTC().Format("2006-01-02 15:04:05Z")
		}
		out = append(out, fmt.Sprintf("%-5d %-24s %s", s.Source.Version, versionName(s.Source.Path), state))
	}
	return out, nil
}

func (m *Migrator) tableExists(ctx context.Context, table string) (bool, error) {
	return tableExists(ctx, m.db, m.driver, table)
}

func versionName(path string) string {
	if path == "" {
		// A Go migration has no file; only the one at version 3 does.
		return "legacy data migration"
	}
	name := path
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return strings.TrimSuffix(name, ".sql")
}

// tableExists asks the dialect's catalogue rather than probing with a query, so
// that a missing table cannot be confused with a permission error.
func tableExists(ctx context.Context, db *sql.DB, driver, table string) (bool, error) {
	var query string
	switch driver {
	case "sqlite":
		query = "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?"
	case "postgres":
		query = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = $1"
	case "mysql":
		query = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?"
	default:
		return false, fmt.Errorf("unsupported driver: %s", driver)
	}

	var n int
	if err := db.QueryRowContext(ctx, query, table).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}
