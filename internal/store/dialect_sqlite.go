package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/slimcdk/subsarr/internal/title"
)

type sqliteDialect struct{}

func (sqliteDialect) placeholder(int) string { return "?" }

// SQLite has no boolean type; the column is an INTEGER holding 0 or 1.
func (sqliteDialect) boolArg(v bool) any {
	if v {
		return 1
	}
	return 0
}

// SQLite's LIKE is already case-insensitive for ASCII.
func (sqliteDialect) likeOp() string { return "LIKE" }

func (sqliteDialect) lengthFn() string { return "LENGTH" }

func (sqliteDialect) upsertSuffix(conflict, update []string) string {
	return upsertSuffixANSI(conflict, update)
}

func (sqliteDialect) titleJoin(b *builder, mode matchMode, query string) (string, string, bool) {
	words := title.Words(query)
	if len(words) == 0 {
		return "", "", false
	}

	switch mode {
	case matchAllWords:
		// Space-separated terms are an implicit AND in FTS5. Each term is quoted
		// so that a title's own punctuation — `-`, `*`, `:` — cannot be read as
		// a query operator.
		terms := make([]string, len(words))
		for i, w := range words {
			terms[i] = ftsQuote(w)
		}
		arg := b.bind(strings.Join(terms, " "))
		// FTS5 rank is more negative the better the match; negate it so that the
		// shared ordering can treat every dialect's score as higher-is-better.
		return "JOIN (SELECT slug, rank AS score FROM titles_fts WHERE titles_fts MATCH " + arg +
			") t ON t.slug = u.slug", "-t.score", true

	default:
		needle := strings.Join(words, " ")
		// The trigram index cannot answer a needle shorter than one trigram; the
		// titles table is small enough to scan for those.
		if len([]rune(needle)) < 3 {
			arg := b.bind("%" + needle + "%")
			return "JOIN (SELECT slug, 0 AS score FROM titles WHERE normalised LIKE " + arg +
				") t ON t.slug = u.slug", "t.score", true
		}
		arg := b.bind(ftsQuote(needle))
		return "JOIN (SELECT slug, 0 AS score FROM titles_trgm WHERE titles_trgm MATCH " + arg +
			") t ON t.slug = u.slug", "t.score", true
	}
}

// ftsQuote wraps a term as an FTS5 string literal.
func ftsQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// refreshTitleIndex fills the two virtual tables from `titles`. FTS5 has no
// generated columns, so the copy is explicit.
func (sqliteDialect) refreshTitleIndex(ctx context.Context, db *sql.DB) error {
	stmts := []string{
		"DELETE FROM titles_fts",
		"INSERT INTO titles_fts (slug, normalised) SELECT slug, normalised FROM titles",
		"INSERT INTO titles_fts(titles_fts) VALUES('optimize')",
		"DELETE FROM titles_trgm",
		"INSERT INTO titles_trgm (slug, normalised) SELECT slug, normalised FROM titles",
		"INSERT INTO titles_trgm(titles_trgm) VALUES('optimize')",
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}

func (sqliteDialect) titleIndexSize(ctx context.Context, db *sql.DB) (int64, error) {
	var n int64
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM titles").Scan(&n)
	return n, err
}

// analyze is what tells SQLite that imdb_id is far more selective than language,
// without which it picks the language index for the query Bazarr asks most.
func (sqliteDialect) analyze(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, "ANALYZE")
	return err
}

// needsAnalyze reports whether ANALYZE has ever run. SQLite reads every index to
// build its statistics, which on five million rows is a minute that a restart
// should not spend when nothing has changed since the last one.
func (sqliteDialect) needsAnalyze(ctx context.Context, db *sql.DB) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'sqlite_stat1'").Scan(&n)
	if err != nil {
		return false, err
	}
	if n == 0 {
		return true, nil
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_stat1").Scan(&n); err != nil {
		return false, err
	}
	return n == 0, nil
}

// checkpoint folds the write-ahead log back into the database file and truncates
// it.
//
// SQLite checkpoints on its own as transactions commit, but only opportunistically
// and never while another connection is reading. An import writes millions of rows
// with random primary keys — every batch dirties pages all over a multi-gigabyte
// file — and reads between batches, so the log grows without bound: on the full
// archive it reached eighteen gigabytes before this was added.
func (sqliteDialect) checkpoint(ctx context.Context, db *sql.DB) error {
	var busy, size, checkpointed int
	if err := db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &size, &checkpointed); err != nil {
		return fmt.Errorf("checkpoint: %w", err)
	}
	if busy != 0 {
		// Something was reading. The next one will get it; saying so is enough.
		return fmt.Errorf("checkpoint blocked by a reader (%d pages still in the log)", size)
	}
	return nil
}
