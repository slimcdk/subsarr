package store

import (
	"context"
	"database/sql"
	"strings"
)

// matchMode is how a free-text query is matched against the title index.
type matchMode int

const (
	// matchAllWords requires every word of the query to appear in the title.
	// It is the mode the title index is built for, and the one all three
	// dialects can express identically.
	matchAllWords matchMode = iota
	// matchSubstring matches anywhere inside a title. It exists for the queries
	// a phrase match cannot answer — a fragment, a word Subscene spelled as part
	// of a longer one — and is only reached when the phrase match found nothing.
	matchSubstring
)

// dialect is the part of the SQL that genuinely differs between databases.
// Everything else — which tables are joined, which filters apply, how results are
// ordered — lives once in sqlStore, so the three databases cannot drift apart.
type dialect interface {
	// placeholder returns the bind marker for the n-th argument (1-based).
	placeholder(n int) string

	// boolArg converts a Go bool into whatever the driver binds to this
	// dialect's boolean column.
	boolArg(v bool) any

	// likeOp returns the case-insensitive substring operator.
	likeOp() string

	// lengthFn returns the function that counts characters (not bytes).
	lengthFn() string

	// upsertSuffix turns an INSERT into an upsert. With no update columns the
	// insert becomes a no-op on conflict.
	upsertSuffix(conflict, update []string) string

	// titleJoin appends its arguments to b and returns a join fragment that
	// binds `t.slug` to the slugs of matching titles, plus an expression that
	// scores the match with higher meaning better. ok is false when this dialect
	// cannot run this mode for this query, in which case the caller falls back.
	titleJoin(b *builder, mode matchMode, query string) (join, score string, ok bool)

	// reindexTitles rebuilds the title index from `uploads`.
	reindexTitles(ctx context.Context, db *sql.DB) error

	// titleIndexSize reports how many titles are indexed, so a start-up check can
	// tell an empty index from a stale one.
	titleIndexSize(ctx context.Context, db *sql.DB) (int64, error)

	// analyze refreshes planner statistics.
	analyze(ctx context.Context, db *sql.DB) error

	// needsAnalyze reports whether statistics are missing altogether. It is what
	// keeps a restart from re-analysing millions of rows that have not changed.
	needsAnalyze(ctx context.Context, db *sql.DB) (bool, error)

	// checkpoint bounds whatever the dialect writes ahead of its data file.
	checkpoint(ctx context.Context, db *sql.DB) error
}

// builder assembles one statement together with its arguments, so that a
// fragment can bind a value without knowing its position.
type builder struct {
	d    dialect
	sql  strings.Builder
	args []any
}

func newBuilder(d dialect) *builder { return &builder{d: d} }

// bind records an argument and returns the placeholder that refers to it.
func (b *builder) bind(v any) string {
	b.args = append(b.args, v)
	return b.d.placeholder(len(b.args))
}

func (b *builder) write(s string) { b.sql.WriteString(s) }

// bindList writes a comma-separated list of placeholders and binds the values
// behind them, for the `IN (…)` lists every batched lookup needs.
func (b *builder) bindList(values []string) {
	for i, v := range values {
		if i > 0 {
			b.write(", ")
		}
		b.write(b.bind(v))
	}
}

func (b *builder) String() string { return b.sql.String() }

// upsertSuffixANSI is the ON CONFLICT form SQLite and PostgreSQL share.
func upsertSuffixANSI(conflict, update []string) string {
	if len(update) == 0 {
		return " ON CONFLICT (" + strings.Join(conflict, ", ") + ") DO NOTHING"
	}
	sets := make([]string, len(update))
	for i, col := range update {
		sets[i] = col + " = excluded." + col
	}
	return " ON CONFLICT (" + strings.Join(conflict, ", ") + ") DO UPDATE SET " + strings.Join(sets, ", ")
}
