package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/slimcdk/subsarr/internal/title"
)

type mysqlDialect struct{}

func (mysqlDialect) name() string { return "mysql" }

func (mysqlDialect) placeholder(int) string { return "?" }

func (mysqlDialect) boolArg(v bool) any { return v }

// The schema's collation is case-insensitive, so LIKE already matches what the
// other dialects do.
func (mysqlDialect) likeOp() string { return "LIKE" }

// LENGTH counts bytes in MySQL; ordering by title length has to count characters
// or a title in a non-Latin script would always sort as the longest.
func (mysqlDialect) lengthFn() string { return "CHAR_LENGTH" }

func (mysqlDialect) upsertSuffix(conflict, update []string) string {
	if len(update) == 0 {
		// MySQL has no DO NOTHING; assigning a column to itself is the idiom.
		return " ON DUPLICATE KEY UPDATE " + conflict[0] + " = " + conflict[0]
	}
	sets := make([]string, len(update))
	for i, col := range update {
		sets[i] = col + " = VALUES(" + col + ")"
	}
	return " ON DUPLICATE KEY UPDATE " + strings.Join(sets, ", ")
}

// innodbMinTokenSize is innodb_ft_min_token_size's default. A shorter word is not
// in the full-text index at all, so requiring it would match nothing.
const innodbMinTokenSize = 3

// innodbStopwords is InnoDB's built-in stopword list. These words are not indexed
// either, and a boolean-mode query that requires one returns no rows — which is
// how "The Matrix" would otherwise find nothing.
var innodbStopwords = map[string]struct{}{
	"a": {}, "about": {}, "an": {}, "are": {}, "as": {}, "at": {}, "be": {}, "by": {},
	"com": {}, "de": {}, "en": {}, "for": {}, "from": {}, "how": {}, "i": {}, "in": {},
	"is": {}, "it": {}, "la": {}, "of": {}, "on": {}, "or": {}, "that": {}, "the": {},
	"this": {}, "to": {}, "was": {}, "what": {}, "when": {}, "where": {}, "who": {},
	"will": {}, "with": {}, "und": {}, "www": {},
}

func (mysqlDialect) titleJoin(b *builder, mode matchMode, query string) (string, string, bool) {
	words := title.Words(query)
	if len(words) == 0 {
		return "", "", false
	}

	switch mode {
	case matchAllWords:
		var terms []string
		for _, w := range words {
			if len([]rune(w)) < innodbMinTokenSize {
				continue
			}
			if _, stop := innodbStopwords[w]; stop {
				continue
			}
			terms = append(terms, "+"+mysqlBooleanTerm(w))
		}
		if len(terms) == 0 {
			// Every word of the query is invisible to the index — "It", "Up",
			// "The Who". The substring path can still answer it.
			return "", "", false
		}
		expr := strings.Join(terms, " ")
		score := b.bind(expr)
		match := b.bind(expr)
		return "JOIN (SELECT slug, MATCH(title) AGAINST (" + score + " IN BOOLEAN MODE) AS score" +
			" FROM titles WHERE MATCH(title) AGAINST (" + match + " IN BOOLEAN MODE)) t ON t.slug = u.slug", "t.score", true

	default:
		arg := b.bind("%" + strings.Join(words, " ") + "%")
		return "JOIN (SELECT slug, 0 AS score FROM titles WHERE title LIKE " + arg +
			") t ON t.slug = u.slug", "t.score", true
	}
}

// mysqlBooleanTerm quotes a word so that boolean-mode operators inside a title —
// `+`, `-`, `*`, `~`, `<`, `>`, `(` — are read as text.
func mysqlBooleanTerm(word string) string {
	return `"` + strings.ReplaceAll(word, `"`, ``) + `"`
}

func (mysqlDialect) reindexTitles(ctx context.Context, db *sql.DB) error {
	stmts := []string{
		"DELETE FROM titles",
		"INSERT INTO titles (slug, title) SELECT slug, MIN(title) FROM uploads WHERE slug <> '' GROUP BY slug",
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}

func (mysqlDialect) titleIndexSize(ctx context.Context, db *sql.DB) (int64, error) {
	var n int64
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM titles").Scan(&n)
	return n, err
}

func (mysqlDialect) analyze(ctx context.Context, db *sql.DB) error {
	for _, table := range []string{"uploads", "files", "titles"} {
		if _, err := db.ExecContext(ctx, "ANALYZE TABLE "+table); err != nil {
			return fmt.Errorf("analyze %s: %w", table, err)
		}
	}
	return nil
}
