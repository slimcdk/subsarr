package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/slimcdk/subsarr/internal/title"
)

type sqliteDialect struct{}

func (sqliteDialect) name() string { return "sqlite" }

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
			return "JOIN (SELECT slug, 0 AS score FROM titles WHERE title LIKE " + arg +
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

func (sqliteDialect) reindexTitles(ctx context.Context, db *sql.DB) error {
	stmts := []string{
		"DELETE FROM titles",
		// One row per slug: the catalogue holds dozens of uploads per work and
		// they all carry the same title.
		"INSERT INTO titles (slug, title) SELECT slug, MIN(title) FROM uploads WHERE slug <> '' GROUP BY slug",
		"DELETE FROM titles_fts",
		"INSERT INTO titles_fts (slug, title) SELECT slug, title FROM titles",
		"INSERT INTO titles_fts(titles_fts) VALUES('optimize')",
		"DELETE FROM titles_trgm",
		"INSERT INTO titles_trgm (slug, title) SELECT slug, title FROM titles",
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
