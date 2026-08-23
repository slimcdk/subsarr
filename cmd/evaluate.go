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
			compare, _ := cmd.Flags().GetInt("compare")
			out := cmd.OutOrStdout()

			s, err := open(ctx, cfg, false)
			if err != nil {
				return err
			}
			defer s.Close()

			ev := &evaluator{store: s.store, db: s.db, driver: cfg.DBDriver, out: out, compare: compare}
			return ev.run(ctx, count)
		},
	}

	cmd.Flags().Int("queries", 2000, "Queries to replay per shape")
	cmd.Flags().Int("compare", 100, "Queries per shape to also run through the substring scan the title index replaced")
	return cmd
}

type evaluator struct {
	store  store.Store
	db     *sql.DB
	driver string
	out    io.Writer
	// compare caps how many queries per shape are also run through the old
	// substring scan. That scan reads every row of the requested language, which
	// is the point of the comparison and the reason it cannot be run on all of
	// them.
	compare int
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
	baseline   []time.Duration // how long that scan took
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
	fmt.Fprintf(e.out, "sampled %d film queries and %d episode queries\n", len(films), len(episodes))

	withIMDB := 0
	for _, s := range films {
		if s.imdb != "" {
			withIMDB++
		}
	}
	fmt.Fprintf(e.out, "%s of the sampled works carry an IMDB id\n\n", percent(withIMDB, len(films)))

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
			p50, p95, _ := percentiles(r.baseline)
			fmt.Fprintf(e.out, "  recall vs substring     %.4f over %d compared queries\n", recall, r.comparable)
			fmt.Fprintf(e.out, "  the scan it replaced    p50 %s, p95 %s over %d queries\n", p50, p95, len(r.baseline))
		}
	}
	return nil
}

func (e *evaluator) replay(ctx context.Context, name string, samples []sample, build func(sample) store.SearchParams) *shapeResult {
	result := &shapeResult{name: name}

	for _, s := range samples {
		params := build(s)

		// A shape whose defining parameter is missing is not that shape: an
		// "IMDB" query with no id is a request for every subtitle in a language,
		// which no client sends and which would be timed as if it were a search.
		if params.ImdbID == "" && params.Query == "" {
			continue
		}

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

		if params.Query == "" || result.comparable >= e.compare {
			continue
		}

		// What the search used to cost: a scan of every upload in the language,
		// matching the query against titles and file names alike.
		baselineStarted := time.Now()
		if _, err := e.substringSearch(ctx, params, true); err != nil {
			continue
		}
		result.baseline = append(result.baseline, time.Since(baselineStarted))

		// And what it used to find. Only titles, because dropping the file-name
		// match was a decision, not a regression — and only when the whole match
		// set fits on one page, or the two paths would be compared on their
		// orderings rather than on what they found.
		baseline, err := e.substringSearch(ctx, params, false)
		if err != nil || len(baseline) >= bazarrPerPage {
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

// argBinder numbers bind parameters for whichever dialect this database is.
type argBinder struct {
	driver string
	args   []any
}

func (b *argBinder) bind(v any) string {
	b.args = append(b.args, v)
	if b.driver == "postgres" {
		return "$" + strconv.Itoa(len(b.args))
	}
	return "?"
}

// substringSearch is the search this service used to do: a scan of every upload
// in the language, matching the query as a substring.
//
// withFilenames reproduces the old path exactly, file names included, which is
// what it cost. Without them it matches titles only, which is what the two paths
// can fairly be compared on: dropping the file-name match was a decision — it is
// why "Dark" used to return every subtitle whose file happened to say so — and
// not a regression this should report as lost recall.
func (e *evaluator) substringSearch(ctx context.Context, p store.SearchParams, withFilenames bool) ([]string, error) {
	var conds []string
	b := &argBinder{driver: e.driver}
	bind := b.bind
	like := "LIKE"
	if e.driver == "postgres" {
		like = "ILIKE"
	}

	if p.Language != "" {
		conds = append(conds, "u.language = "+bind(p.Language))
	}
	pattern := "%" + p.Query + "%"
	if withFilenames {
		conds = append(conds, "(u.title "+like+" "+bind(pattern)+" OR f.filename "+like+" "+bind(pattern)+")")
	} else {
		conds = append(conds, "u.title "+like+" "+bind(pattern))
	}

	query := "SELECT f.id FROM files f JOIN uploads u ON u.id = f.upload_id WHERE " +
		strings.Join(conds, " AND ") + " ORDER BY f.downloads DESC LIMIT " + bind(bazarrPerPage)

	rows, err := e.db.QueryContext(ctx, query, b.args...)
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

// sampleFilms takes queries from the title index: one upload per distinct work,
// spread across the catalogue.
//
// Sampling from `titles` rather than from `uploads` is what keeps this cheap: the
// index holds one row per work and is read a page at a time, where grouping the
// whole upload table would be minutes of work before a single query is replayed.
func (e *evaluator) sampleFilms(ctx context.Context, count int) ([]sample, error) {
	return e.sampleQuery(ctx, count, "")
}

// sampleEpisodes takes queries from works whose release names look like an
// episode, which is how Bazarr's TV searches are shaped.
func (e *evaluator) sampleEpisodes(ctx context.Context, count int) ([]sample, error) {
	samples, err := e.sampleQuery(ctx, count, "AND u.releases LIKE '%S0%E0%'")
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

// sampleQuery walks the title index in pages, taking one upload per work and
// keeping every seventh one so the sample is not one corner of the catalogue.
func (e *evaluator) sampleQuery(ctx context.Context, count int, extra string) ([]sample, error) {
	const page = 500

	out := make([]sample, 0, count)
	last := ""
	seen := 0

	for len(out) < count {
		b := &argBinder{driver: e.driver}
		query := "SELECT t.slug, MIN(t.title), MIN(u.imdb_id), MIN(u.language)" +
			" FROM titles t JOIN uploads u ON u.slug = t.slug" +
			" JOIN files f ON f.upload_id = u.id" +
			" WHERE t.slug > " + b.bind(last) + " " + extra +
			" GROUP BY t.slug ORDER BY t.slug LIMIT " + b.bind(page)

		rows, err := e.db.QueryContext(ctx, query, b.args...)
		if err != nil {
			return nil, fmt.Errorf("sample: %w", err)
		}

		found := 0
		for rows.Next() {
			var s sample
			if err := rows.Scan(&s.slug, &s.title, &s.imdb, &s.language); err != nil {
				rows.Close()
				return nil, err
			}
			found++
			last = s.slug
			if seen%7 == 0 && len(out) < count {
				out = append(out, s)
			}
			seen++
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if found == 0 {
			break
		}
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
