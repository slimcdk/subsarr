// Package sqlitedriver configures a custom SQLite driver that compiles in
// FTS5, STAT4, SPELLFIX1, and REGEXP support.
//
// The driver is registered under the name "sqlite3_subsarr" and used
// when SUBSARR_DB_DRIVER=sqlite.
package sqlitedriver

import (
	"database/sql"
	"regexp"

	sqlite3 "github.com/mattn/go-sqlite3"
)

const driverName = "sqlite3_subsarr"

func init() {
	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
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

			return conn.RegisterFunc("regexp", func(pattern, s string) (bool, error) {
				return regexp.MatchString(pattern, s)
			}, true)
		},
	})
}
