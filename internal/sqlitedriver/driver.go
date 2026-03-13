// Package sqlitedriver configures a custom SQLite driver for PocketBase that
// compiles in FTS5, STAT4, SPELLFIX1, and REGEXP support.
//
// FTS5 and STAT4 are compiled into SQLite at build time via the
// sqlite_fts5 and sqlite_stat4 build tags (see Dockerfile).
//
// SPELLFIX1 is registered as a SQLite auto-extension so it is available on
// every database connection automatically.
//
// REGEXP is added as a user-defined SQL function via the ConnectHook so that
//
//	WHERE column REGEXP 'pattern'
//
// works anywhere in PocketBase queries.
package sqlitedriver

import (
	"database/sql"
	"regexp"

	sqlite3 "github.com/mattn/go-sqlite3"
	"github.com/pocketbase/dbx"
)

const driverName = "sqlite3_subsarr"

func init() {
	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			// Pragmas — mirror PocketBase's DefaultDBConnect settings exactly.
			pragmas := []string{
				"PRAGMA busy_timeout       = 10000",
				"PRAGMA journal_mode       = WAL",
				"PRAGMA journal_size_limit = 200000000",
				"PRAGMA synchronous        = NORMAL",
				"PRAGMA foreign_keys       = ON",
				"PRAGMA temp_store         = MEMORY",
				"PRAGMA cache_size         = -32000",
			}
			for _, p := range pragmas {
				if _, err := conn.Exec(p, nil); err != nil {
					return err
				}
			}

			// REGEXP: enables  WHERE col REGEXP 'pattern'
			return conn.RegisterFunc("regexp", func(pattern, s string) (bool, error) {
				return regexp.MatchString(pattern, s)
			}, true)
		},
	})
}

// Connect opens a PocketBase-compatible dbx.DB using the custom SQLite driver.
// It is passed as pocketbase.Config.DBConnect in main.go.
func Connect(dbPath string) (*dbx.DB, error) {
	sqlDB, err := sql.Open(driverName, dbPath)
	if err != nil {
		return nil, err
	}
	return dbx.NewFromDB(sqlDB, "sqlite3"), nil
}
