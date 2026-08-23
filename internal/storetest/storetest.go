// Package storetest gives every package that needs a database a real, migrated
// one: SQLite always, and PostgreSQL and MySQL whenever this environment can
// reach them. The schema comes from the production migrations, so a test can
// never pass against a schema that does not exist.
package storetest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/slimcdk/subsarr/internal/database"
	"github.com/slimcdk/subsarr/internal/store"
)

// Test DSNs for the two servers that cannot be created out of thin air. When
// they are unset the suite still runs — against SQLite only — so a contributor
// without Docker can work, while CI sets both and proves dialect parity.
const (
	envPostgresDSN = "SUBSARR_TEST_POSTGRES_DSN"
	envMySQLDSN    = "SUBSARR_TEST_MYSQL_DSN"

	// envRequire makes a missing DSN a failure rather than a skip. CI sets it,
	// so that dropping the service block from a workflow cannot quietly reduce
	// the suite to SQLite while it still gates image publication.
	envRequire = "SUBSARR_TEST_REQUIRE_DIALECTS"
)

var testDBCounter atomic.Int64

// Each runs fn against every database this environment can reach, each with its
// own empty, fully migrated schema. It is the seam the conformance suite hangs
// off: one body of assertions, three databases.
func Each(t *testing.T, fn func(t *testing.T, st store.Store)) {
	t.Helper()

	t.Run("sqlite", func(t *testing.T) {
		fn(t, SQLite(t))
	})

	if dsn := os.Getenv(envPostgresDSN); dsn != "" {
		t.Run("postgres", func(t *testing.T) {
			fn(t, newStore(t, "postgres", postgresTestDSN(t, dsn)))
		})
	} else {
		missing(t, envPostgresDSN, "postgres")
	}

	if dsn := os.Getenv(envMySQLDSN); dsn != "" {
		t.Run("mysql", func(t *testing.T) {
			fn(t, newStore(t, "mysql", mysqlTestDSN(t, dsn)))
		})
	} else {
		missing(t, envMySQLDSN, "mysql")
	}
}

func missing(t *testing.T, env, dialect string) {
	t.Helper()
	if os.Getenv(envRequire) != "" {
		t.Fatalf("%s is set but %s is not: %s cannot be covered", envRequire, env, dialect)
	}
	t.Logf("skipping %s: %s not set", dialect, env)
}

// SQLite returns a migrated store on a throwaway file. Every environment can run
// it, so it is the one database the whole suite always covers.
func SQLite(t *testing.T) store.Store {
	t.Helper()
	return newStore(t, "sqlite", filepath.Join(t.TempDir(), "subsarr-test.db"))
}

func newStore(t *testing.T, driver, dsn string) store.Store {
	t.Helper()

	db, err := database.Open(driver, dsn)
	if err != nil {
		t.Fatalf("open %s: %v", driver, err)
	}
	t.Cleanup(func() { db.Close() })

	migrator, err := database.NewMigrator(db, driver)
	if err != nil {
		t.Fatalf("migrator %s: %v", driver, err)
	}
	if err := migrator.Up(context.Background()); err != nil {
		t.Fatalf("migrate %s: %v", driver, err)
	}

	st, err := store.New(db, driver)
	if err != nil {
		t.Fatalf("store %s: %v", driver, err)
	}
	return st
}

// postgresTestDSN creates a throwaway schema on the configured server. A schema
// is enough isolation and is far cheaper than a database per test.
func postgresTestDSN(t *testing.T, dsn string) string {
	t.Helper()

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer admin.Close()

	schema := testSchemaName()
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			return
		}
		defer db.Close()
		_, _ = db.Exec("DROP SCHEMA " + schema + " CASCADE")
	})

	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "search_path=" + schema
}

// mysqlTestDSN creates a throwaway database: MySQL's schemas and databases are
// the same thing, so there is no cheaper unit of isolation.
func mysqlTestDSN(t *testing.T, dsn string) string {
	t.Helper()

	admin, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open mysql: %v", err)
	}
	defer admin.Close()

	name := testSchemaName()
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			return
		}
		defer db.Close()
		_, _ = db.Exec("DROP DATABASE " + name)
	})

	base, params, _ := strings.Cut(dsn, "?")
	slash := strings.LastIndex(base, "/")
	out := base[:slash+1] + name
	if params != "" {
		out += "?" + params
	}
	return out
}

func testSchemaName() string {
	return fmt.Sprintf("subsarr_test_%d_%d", os.Getpid(), testDBCounter.Add(1))
}
