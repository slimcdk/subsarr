package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/slimcdk/subsarr/internal/title"
)

type mysqlDialect struct{}

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
		var terms, unindexed []string
		for _, w := range words {
			if len([]rune(w)) < innodbMinTokenSize {
				unindexed = append(unindexed, w)
				continue
			}
			if _, stop := innodbStopwords[w]; stop {
				unindexed = append(unindexed, w)
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

		// A word the index cannot hold is still a word the query asked for.
		// Without this, "24 - Eighth Season" drops the 24 and matches every
		// eighth season there is — which is 221 results where the other two
		// dialects find 203. The full-text match has already narrowed the rows,
		// so filtering them costs nothing.
		var filters strings.Builder
		for _, w := range unindexed {
			filters.WriteString(" AND normalised LIKE " + b.bind("%"+w+"%"))
		}

		return "JOIN (SELECT slug, MATCH(normalised) AGAINST (" + score + " IN BOOLEAN MODE) AS score" +
			" FROM titles WHERE MATCH(normalised) AGAINST (" + match + " IN BOOLEAN MODE)" + filters.String() +
			") t ON t.slug = u.slug", "t.score", true

	default:
		arg := b.bind("%" + strings.Join(words, " ") + "%")
		return "JOIN (SELECT slug, 0 AS score FROM titles WHERE normalised LIKE " + arg +
			") t ON t.slug = u.slug", "t.score", true
	}
}

// mysqlBooleanTerm quotes a word so that boolean-mode operators inside a title —
// `+`, `-`, `*`, `~`, `<`, `>`, `(` — are read as text.
func mysqlBooleanTerm(word string) string {
	return `"` + strings.ReplaceAll(word, `"`, ``) + `"`
}

// refreshTitleIndex has nothing to do: InnoDB maintains the full-text key on
// `normalised` as rows are written.
func (mysqlDialect) refreshTitleIndex(context.Context, *sql.DB) error { return nil }

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

// needsAnalyze is always false: InnoDB samples index statistics on its own, and
// an import refreshes them explicitly when it finishes.
func (mysqlDialect) needsAnalyze(context.Context, *sql.DB) (bool, error) { return false, nil }

// checkpoint is a no-op: InnoDB manages its own redo log.
func (mysqlDialect) checkpoint(context.Context, *sql.DB) error { return nil }
