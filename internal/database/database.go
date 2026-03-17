package database

import (
	"database/sql"
	"embed"
	"fmt"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/slimcdk/subsarr/internal/sqlitedriver"
)

//go:embed migrations
var migrations embed.FS

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

// Migrate runs the schema migration for the configured dialect.
func Migrate(db *sql.DB, driver string) error {
	var file string
	switch driver {
	case "sqlite":
		file = "migrations/sqlite.sql"
	case "postgres":
		file = "migrations/postgres.sql"
	case "mysql":
		file = "migrations/mysql.sql"
	default:
		return fmt.Errorf("unsupported driver: %s", driver)
	}
	data, err := migrations.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read migration: %w", err)
	}
	_, err = db.Exec(string(data))
	return err
}
