// Package sqlitedriver registers the SQLite driver subsarr uses, named
// "sqlite3_subsarr". It is the stock mattn/go-sqlite3 driver built with FTS5 and
// STAT4 (see the build tags in the Makefile), plus the connection settings a
// read-mostly database of several million rows needs.
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
