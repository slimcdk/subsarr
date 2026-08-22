package cmd

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/slimcdk/subsarr/internal/config"
	"github.com/slimcdk/subsarr/internal/store"
	"github.com/spf13/cobra"
)

// bazarrPerPage is what Bazarr's subsarr provider asks for. Latency has to be
// measured on the page size the real client uses.
const bazarrPerPage = 100

func evaluateCommand(cfg config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "evaluate-search",
		Short: "Replay Bazarr-style queries against this database and report recall and latency",
		Long: `Replay Bazarr-style queries against a real database.

Queries are sampled from the data itself, in the four shapes Bazarr sends: a film
by IMDB id, an episode by series IMDB id with season and episode, a film by
title, and a title with season and episode. Each is timed at the page size Bazarr
asks for.

Title queries are also compared against the substring scan the service used
before the title index existed, so that "faster" can be shown not to mean "finds
less". A title query that does not return the work it was taken from is reported
as an exact-title miss; there should be none.

Nothing is written. Run it on a copy of a real database:

  subsarr evaluate-search --queries 2000`,
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			count, _ := cmd.Flags().GetInt("queries")
			out := cmd.OutOrStdout()

			s, err := open(ctx, cfg, false)
			if err != nil {
				return err
			}
			defer s.Close()

			ev := &evaluator{store: s.store, db: s.db, driver: cfg.DBDriver, out: out}
			return ev.run(ctx, count)
		},
	}

	cmd.Flags().Int("queries", 2000, "Queries to replay per shape")
	return cmd
}

type evaluator struct {
	store  store.Store
	db     *sql.DB
	driver string
	out    io.Writer
}

// sample is one query taken from the data, together with what it was taken from.
type sample struct {
	imdb     string
	title    string
	slug     string
	language string
	season   int
	episode  int
}

type shapeResult struct {
	name       string
	queries    int
	hits       int
	latencies  []time.Duration
	checked    int // queries taken from a known upload
	misses     int // those that did not return it
	recallLost int // results the old substring path found and this one did not
	comparable int
}

func (e *evaluator) run(ctx context.Context, count int) error {
	films, err := e.sampleFilms(ctx, count)
	if err != nil {
		return err
	}
	episodes, err := e.sampleEpisodes(ctx, count)
	if err != nil {
		return err
	}
	if len(films) == 0 {
		return fmt.Errorf("no uploads to sample from — is the catalogue loaded?")
	}
	fmt.Fprintf(e.out, "sampled %d film queries and %d episode queries\n\n", len(films), len(episodes))

	results := []*shapeResult{
		e.replay(ctx, "imdb", films, func(s sample) store.SearchParams {
			return store.SearchParams{ImdbID: s.imdb, Language: s.language, Limit: bazarrPerPage}
		}),
		e.replay(ctx, "imdb+episode", episodes, func(s sample) store.SearchParams {
			return store.SearchParams{
				ImdbID: s.imdb, Language: s.language, Limit: bazarrPerPage,
				SeasonEp: fmt.Sprintf("S%02dE%02d", s.season, s.episode),
			}
		}),
		e.replay(ctx, "title", films, func(s sample) store.SearchParams {
			return store.SearchParams{Query: s.title, Language: s.language, Limit: bazarrPerPage}
		}),
		e.replay(ctx, "title+episode", episodes, func(s sample) store.SearchParams {
			return store.SearchParams{
				Query: s.title, Language: s.language, Limit: bazarrPerPage,
				SeasonEp: fmt.Sprintf("S%02dE%02d", s.season, s.episode),
			}
		}),
		// The miss is the common case in a real library, and it has to be the
		// cheapest thing the service does.
		e.replay(ctx, "title miss", missQueries(films), func(s sample) store.SearchParams {
			return store.SearchParams{Query: s.title, Language: s.language, Limit: bazarrPerPage}
		}),
	}

	fmt.Fprintf(e.out, "%-16s %8s %8s %10s %10s %10s\n", "SHAPE", "QUERIES", "HIT %", "p50", "p95", "max")
	for _, r := range results {
		if r.queries == 0 {
			continue
		}
		p50, p95, max := percentiles(r.latencies)
		fmt.Fprintf(e.out, "%-16s %8d %7.1f%% %10s %10s %10s\n",
			r.name, r.queries, float64(r.hits)*100/float64(r.queries), p50, p95, max)
	}

	for _, r := range results {
		if r.checked == 0 {
			continue
		}
		fmt.Fprintf(e.out, "\n%s\n", r.name)
		fmt.Fprintf(e.out, "  recall                  %.4f — %d of %d queries did not return the upload they were taken from\n",
			float64(r.checked-r.misses)/float64(r.checked), r.misses, r.checked)
		if r.comparable > 0 {
			recall := float64(r.comparable-r.recallLost) / float64(r.comparable)
			fmt.Fprintf(e.out, "  recall vs substring     %.4f over %d compared queries\n", recall, r.comparable)
		}
	}
	return nil
}

func (e *evaluator) replay(ctx context.Context, name string, samples []sample, build func(sample) store.SearchParams) *shapeResult {
	result := &shapeResult{name: name}

	for _, s := range samples {
		params := build(s)

		started := time.Now()
		subs, total, err := e.store.SearchSubtitles(ctx, params)
		elapsed := time.Since(started)
		if err != nil {
			fmt.Fprintf(e.out, "query failed (%s %q): %v\n", name, params.Query, err)
			continue
		}

		result.queries++
		result.latencies = append(result.latencies, elapsed)
		if total > 0 {
			result.hits++
		}

		if s.slug == "" {
			continue
		}

		// The work the query was taken from has to be in the answer. For the
		// IMDB shapes this is the recall measure: the query is the id of an
		// upload that exists, so a miss is the service failing to find its own
		// row.
		result.checked++
		found := false
		for _, sub := range subs {
			if sub.Slug == s.slug {
				found = true
				break
			}
		}
		if !found {
			result.misses++
		}

		if params.Query == "" {
			continue
		}

		// And the index must not find less than the scan it replaced.
		baseline, err := e.substringSearch(ctx, params)
		if err != nil {
			continue
		}
		result.comparable++
		got := make(map[string]struct{}, len(subs))
		for _, sub := range subs {
			got[sub.ID] = struct{}{}
		}
		for _, id := range baseline {
			if _, ok := got[id]; !ok {
				result.recallLost++
				break
			}
		}
	}
	return result
}

// substringSearch is the search this service used to do: a scan of every upload
// in the language, matching the query as a substring of the title or the file
// name. It is the baseline the title index is measured against.
func (e *evaluator) substringSearch(ctx context.Context, p store.SearchParams) ([]string, error) {
	var (
		conds []string
		args  []any
	)
	bind := func(v any) string {
		args = append(args, v)
		if e.driver == "postgres" {
			return "$" + strconv.Itoa(len(args))
		}
		return "?"
	}
	like := "LIKE"
	if e.driver == "postgres" {
		like = "ILIKE"
	}

	if p.Language != "" {
		conds = append(conds, "u.language = "+bind(p.Language))
	}
	pattern := "%" + p.Query + "%"
	conds = append(conds, "(u.title "+like+" "+bind(pattern)+" OR f.filename "+like+" "+bind(pattern)+")")

	query := "SELECT f.id FROM files f JOIN uploads u ON u.id = f.upload_id WHERE " +
		strings.Join(conds, " AND ") + " ORDER BY f.downloads DESC LIMIT " + bind(bazarrPerPage)

	rows, err := e.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// sampleFilms takes queries from uploads that carry an IMDB id, spread across the
// table rather than taken from its start.
func (e *evaluator) sampleFilms(ctx context.Context, count int) ([]sample, error) {
	const withIMDB = `SELECT u.imdb_id, u.title, u.slug, u.language
		FROM uploads u JOIN files f ON f.upload_id = u.id
		WHERE u.imdb_id <> '' AND u.title <> ''
		GROUP BY u.imdb_id, u.title, u.slug, u.language`

	samples, err := e.sampleQuery(ctx, withIMDB, count)
	if err != nil || len(samples) > 0 {
		return samples, err
	}

	// An installation that has been migrated but not yet re-imported has no
	// IMDB ids at all. The title shapes are still worth measuring.
	const anyUpload = `SELECT u.imdb_id, u.title, u.slug, u.language
		FROM uploads u JOIN files f ON f.upload_id = u.id
		WHERE u.title <> ''
		GROUP BY u.imdb_id, u.title, u.slug, u.language`
	return e.sampleQuery(ctx, anyUpload, count)
}

// sampleEpisodes takes queries from uploads whose release names look like an
// episode, which is how Bazarr's TV searches are shaped.
func (e *evaluator) sampleEpisodes(ctx context.Context, count int) ([]sample, error) {
	const query = `SELECT u.imdb_id, u.title, u.slug, u.language
		FROM uploads u JOIN files f ON f.upload_id = u.id
		WHERE u.imdb_id <> '' AND u.releases LIKE '%S0%E0%'
		GROUP BY u.imdb_id, u.title, u.slug, u.language`

	samples, err := e.sampleQuery(ctx, query, count)
	if err != nil {
		return nil, err
	}
	for i := range samples {
		// A spread of episodes rather than the first one of every series.
		samples[i].season = 1 + i%5
		samples[i].episode = 1 + i%12
	}
	return samples, nil
}

func (e *evaluator) sampleQuery(ctx context.Context, query string, count int) ([]sample, error) {
	rows, err := e.db.QueryContext(ctx, query+" LIMIT "+strconv.Itoa(count*7))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var all []sample
	for rows.Next() {
		var s sample
		if err := rows.Scan(&s.imdb, &s.title, &s.slug, &s.language); err != nil {
			return nil, err
		}
		all = append(all, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Every seventh row, so the sample is not one corner of the table.
	out := make([]sample, 0, count)
	for i := 0; i < len(all) && len(out) < count; i += 7 {
		out = append(out, all[i])
	}
	return out, nil
}

// missQueries turns real titles into titles that are not in the data, which is
// what most of a library's searches are.
func missQueries(samples []sample) []sample {
	out := make([]sample, 0, len(samples))
	for _, s := range samples {
		s.title += " Nonexistent Sequel Part Nine"
		s.slug = ""
		out = append(out, s)
	}
	return out
}

func percentiles(values []time.Duration) (p50, p95, max time.Duration) {
	if len(values) == 0 {
		return 0, 0, 0
	}
	sorted := make([]time.Duration, len(values))
	copy(sorted, values)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	at := func(q float64) time.Duration {
		i := int(float64(len(sorted)-1) * q)
		return sorted[i].Round(time.Microsecond)
	}
	return at(0.50), at(0.95), sorted[len(sorted)-1].Round(time.Microsecond)
}
