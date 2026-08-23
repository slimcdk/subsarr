package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/slimcdk/subsarr/internal/title"
)

type postgresDialect struct{}

func (postgresDialect) placeholder(n int) string { return "$" + strconv.Itoa(n) }

func (postgresDialect) boolArg(v bool) any { return v }

// PostgreSQL's LIKE is case-sensitive, so substring filters use ILIKE to match
// what the other two dialects do by collation.
func (postgresDialect) likeOp() string { return "ILIKE" }

func (postgresDialect) lengthFn() string { return "LENGTH" }

func (postgresDialect) upsertSuffix(conflict, update []string) string {
	return upsertSuffixANSI(conflict, update)
}

func (postgresDialect) titleJoin(b *builder, mode matchMode, query string) (string, string, bool) {
	words := title.Words(query)
	if len(words) == 0 {
		return "", "", false
	}
	needle := strings.Join(words, " ")

	switch mode {
	case matchAllWords:
		// plainto_tsquery ANDs the lexemes, which is the same recall the other
		// dialects give. The 'simple' configuration is deliberate: stemming a
		// film title changes what it means.
		rank := b.bind(needle)
		match := b.bind(needle)
		return "JOIN (SELECT slug, ts_rank(tsv, plainto_tsquery('simple', " + rank + ")) AS score" +
			" FROM titles WHERE tsv @@ plainto_tsquery('simple', " + match + ")) t ON t.slug = u.slug", "t.score", true

	default:
		arg := b.bind("%" + needle + "%")
		return "JOIN (SELECT slug, 0 AS score FROM titles WHERE normalised ILIKE " + arg +
			") t ON t.slug = u.slug", "t.score", true
	}
}

// refreshTitleIndex has nothing to do: the searched column is generated from
// `normalised` and the GIN index follows it.
func (postgresDialect) refreshTitleIndex(context.Context, *sql.Tx) error { return nil }

func (postgresDialect) titleIndexSize(ctx context.Context, db *sql.DB) (int64, error) {
	var n int64
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM titles").Scan(&n)
	return n, err
}

// analyze runs explicitly after an import rather than waiting for autovacuum,
// which would otherwise leave the first hours of searches on stale statistics.
func (postgresDialect) analyze(ctx context.Context, db *sql.DB) error {
	for _, table := range []string{"uploads", "files", "titles"} {
		if _, err := db.ExecContext(ctx, "ANALYZE "+table); err != nil {
			return fmt.Errorf("analyze %s: %w", table, err)
		}
	}
	return nil
}

// needsAnalyze is always false: autovacuum keeps PostgreSQL's statistics current
// on its own, and an import refreshes them explicitly when it finishes.
func (postgresDialect) needsAnalyze(context.Context, *sql.DB) (bool, error) { return false, nil }

// checkpoint is a no-op: PostgreSQL manages its own write-ahead log.
func (postgresDialect) checkpoint(context.Context, *sql.DB) error { return nil }
