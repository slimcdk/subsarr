package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	. "github.com/slimcdk/subsarr/internal/store"
	"github.com/slimcdk/subsarr/internal/storetest"
)

// The conformance suite. Every case here runs against every database this
// environment can reach, through the Store interface only, so that "PostgreSQL
// and MySQL behave like SQLite" is proven rather than assumed.

func upload(id, slug, title, imdb, language string, opts ...func(*Upload)) Upload {
	u := Upload{
		ID: id, SubsceneID: id, FilePath: "Subscene Files DB/" + slug + "/" + id + ".zip",
		Slug: slug, Title: title, ImdbID: imdb, Language: language, Releases: "[]",
	}
	for _, opt := range opts {
		opt(&u)
	}
	return u
}

func withHI(u *Upload)                    { u.HI = true }
func withYear(y int) func(*Upload)        { return func(u *Upload) { u.Year = y } }
func withReleases(r string) func(*Upload) { return func(u *Upload) { u.Releases = r } }

func file(id, uploadID, filename, hash string) IngestFile {
	return IngestFile{
		ID: id, UploadID: uploadID, Filename: filename, Format: "srt",
		ContentHash: hash, ContentKey: "content/" + hash[:2] + "/" + hash[2:4] + "/" + hash + ".srt",
		Size: 1024,
	}
}

func seed(t *testing.T, st Store, uploads []Upload, files []IngestFile) {
	t.Helper()
	ctx := context.Background()
	if err := st.UpsertUploads(ctx, uploads); err != nil {
		t.Fatalf("UpsertUploads: %v", err)
	}
	if err := st.IngestFiles(ctx, files); err != nil {
		t.Fatalf("IngestFiles: %v", err)
	}
	if err := st.Reindex(ctx); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
}

// hash produces a distinct 64-character hex string per seed value.
func hash(n int) string {
	return strings.Repeat(fmt.Sprintf("%02x", n%256), 32)
}

func ids(subs []Subtitle) []string {
	out := make([]string, len(subs))
	for i, s := range subs {
		out[i] = s.ID
	}
	return out
}

// darkKnight is the fixture most cases share: one film under two slugs (Subscene
// filed the same work twice), in two languages, plus an unrelated film and a
// series.
func darkKnight(t *testing.T, st Store) {
	t.Helper()
	seed(t, st,
		[]Upload{
			upload("101", "the-dark-knight", "The Dark Knight", "tt0468569", "english", withYear(2008)),
			upload("102", "the-dark-knight", "The Dark Knight", "tt0468569", "danish", withYear(2008)),
			upload("103", "batman-the-dark-knight", "Batman The Dark Knight", "tt0468569", "english", withYear(2008)),
			upload("104", "the-dark-knight-rises", "The Dark Knight Rises", "tt1345836", "english", withYear(2012)),
			upload("105", "the-dark-knight", "The Dark Knight", "tt0468569", "english", withHI, withYear(2008)),
			upload("201", "breaking-bad-second-season", "Breaking Bad Second Season", "tt0903747", "english",
				withReleases(`["Breaking.Bad.S02E05.720p.HDTV"]`)),
		},
		[]IngestFile{
			file("f101", "101", "The.Dark.Knight.2008.720p.srt", hash(1)),
			file("f102", "102", "The.Dark.Knight.2008.dan.srt", hash(2)),
			file("f103", "103", "Batman.The.Dark.Knight.srt", hash(3)),
			file("f104", "104", "The.Dark.Knight.Rises.srt", hash(4)),
			file("f105", "105", "The.Dark.Knight.HI.srt", hash(5)),
			file("f201", "201", "Breaking.Bad.S02E05.srt", hash(6)),
		},
	)
}

func TestConformance_SearchByIMDBAndLanguage(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st Store) {
		darkKnight(t, st)

		subs, total, err := st.SearchSubtitles(context.Background(), SearchParams{
			ImdbID: "tt0468569", Language: "english", Limit: 50,
		})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if total != 3 {
			t.Errorf("total = %d, want 3 (two slugs plus the HI upload)", total)
		}
		for _, s := range subs {
			if s.ImdbID != "tt0468569" || s.Language != "english" {
				t.Errorf("unexpected result %+v", s)
			}
		}
	})
}

func TestConformance_SearchBySeasonEpisode(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st Store) {
		darkKnight(t, st)
		ctx := context.Background()

		subs, total, err := st.SearchSubtitles(ctx, SearchParams{
			ImdbID: "tt0903747", Language: "english", SeasonEp: "S02E05", Limit: 50,
		})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if total != 1 || len(subs) != 1 || subs[0].ID != "f201" {
			t.Fatalf("got %v (total %d), want [f201]", ids(subs), total)
		}

		_, total, err = st.SearchSubtitles(ctx, SearchParams{
			ImdbID: "tt0903747", Language: "english", SeasonEp: "S03E01", Limit: 50,
		})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if total != 0 {
			t.Errorf("total = %d, want 0 for an episode that is not in the release list", total)
		}
	})
}

// Subscene filed The Dark Knight under a variant slug as well. A title search has
// to find both, or those subtitles are invisible to Bazarr.
func TestConformance_SearchByTitleFindsVariantSlugs(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st Store) {
		darkKnight(t, st)

		subs, total, err := st.SearchSubtitles(context.Background(), SearchParams{
			Query: "The Dark Knight", Language: "english", Limit: 50,
		})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if total != 4 {
			t.Fatalf("total = %d, want 4 (both slugs, the HI upload and the sequel): %v", total, ids(subs))
		}

		// Exact title first, then the closest titles: Bazarr reads the top of
		// the list and scores what it finds there.
		var order []string
		for _, s := range subs {
			order = append(order, s.Slug)
		}
		want := []string{"the-dark-knight", "the-dark-knight", "the-dark-knight-rises", "batman-the-dark-knight"}
		for i := range want {
			if order[i] != want[i] {
				t.Fatalf("order = %v, want %v (exact title first, then shortest)", order, want)
			}
		}
	})
}

func TestConformance_SearchByTitleIsCaseAndPunctuationInsensitive(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st Store) {
		darkKnight(t, st)

		for _, query := range []string{"the dark knight", "THE DARK KNIGHT", "the-dark-knight", "The  Dark  Knight"} {
			_, total, err := st.SearchSubtitles(context.Background(), SearchParams{
				Query: query, Language: "english", Limit: 50,
			})
			if err != nil {
				t.Fatalf("search %q: %v", query, err)
			}
			if total != 4 {
				t.Errorf("query %q found %d, want 4", query, total)
			}
		}
	})
}

// A query no word match can answer — every word is below the full-text index's
// minimum token length — must still find the title through the substring path.
func TestConformance_SearchByTitleFallsBackToSubstring(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st Store) {
		seed(t, st,
			[]Upload{upload("301", "up", "Up", "tt1049413", "english")},
			[]IngestFile{file("f301", "301", "Up.2009.srt", hash(7))},
		)

		subs, total, err := st.SearchSubtitles(context.Background(), SearchParams{
			Query: "Up", Language: "english", Limit: 50,
		})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if total != 1 || len(subs) != 1 || subs[0].ID != "f301" {
			t.Errorf("got %v (total %d), want [f301]", ids(subs), total)
		}
	})
}

// A fragment inside a word is what the substring path exists for: no word match
// can find it, and Subscene spelled plenty of titles as one word.
func TestConformance_SearchByTitleFindsAFragment(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st Store) {
		seed(t, st,
			[]Upload{upload("302", "spiderman-homecoming", "Spiderman Homecoming", "tt2250912", "english")},
			[]IngestFile{file("f302", "302", "Spiderman.srt", hash(11))},
		)

		subs, total, err := st.SearchSubtitles(context.Background(), SearchParams{
			Query: "iderman", Language: "english", Limit: 50,
		})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if total != 1 || len(subs) != 1 || subs[0].ID != "f302" {
			t.Errorf("got %v (total %d), want [f302]", ids(subs), total)
		}
	})
}

func TestConformance_SearchMissReturnsEmptyPage(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st Store) {
		darkKnight(t, st)

		subs, total, err := st.SearchSubtitles(context.Background(), SearchParams{
			Query: "A Film Nobody Uploaded", Language: "english", Limit: 50,
		})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if total != 0 || len(subs) != 0 {
			t.Errorf("got %d results (total %d), want none", len(subs), total)
		}
	})
}

func TestConformance_HearingImpairedIsTriState(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st Store) {
		darkKnight(t, st)
		ctx := context.Background()
		yes, no := true, false

		_, both, err := st.SearchSubtitles(ctx, SearchParams{ImdbID: "tt0468569", Language: "english", Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		_, onlyHI, err := st.SearchSubtitles(ctx, SearchParams{ImdbID: "tt0468569", Language: "english", HI: &yes, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		_, onlyNot, err := st.SearchSubtitles(ctx, SearchParams{ImdbID: "tt0468569", Language: "english", HI: &no, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}

		if both != 3 || onlyHI != 1 || onlyNot != 2 {
			t.Errorf("both=%d hi=%d not-hi=%d, want 3/1/2", both, onlyHI, onlyNot)
		}
	})
}

// A catalogue row without a year must not be filtered out by a year: the year is
// unknown, not different.
func TestConformance_YearMatchesExactlyOrUnknown(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st Store) {
		seed(t, st,
			[]Upload{
				upload("401", "dune", "Dune", "tt1160419", "english", withYear(2021)),
				upload("402", "dune", "Dune", "tt1160419", "english"), // year unknown
				upload("403", "dune", "Dune", "tt0087182", "english", withYear(1984)),
			},
			[]IngestFile{
				file("f401", "401", "Dune.2021.srt", hash(8)),
				file("f402", "402", "Dune.srt", hash(9)),
				file("f403", "403", "Dune.1984.srt", hash(10)),
			},
		)

		_, total, err := st.SearchSubtitles(context.Background(), SearchParams{
			Query: "Dune", Language: "english", Year: 2021, Limit: 50,
		})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if total != 2 {
			t.Errorf("total = %d, want 2 (the 2021 upload and the one with no year)", total)
		}
	})
}

func TestConformance_PaginationReportsExactTotal(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st Store) {
		var uploads []Upload
		var files []IngestFile
		for i := range 25 {
			id := fmt.Sprintf("5%02d", i)
			uploads = append(uploads, upload(id, "the-matrix", "The Matrix", "tt0133093", "english"))
			files = append(files, file("f"+id, id, fmt.Sprintf("The.Matrix.%d.srt", i), hash(20+i)))
		}
		seed(t, st, uploads, files)

		subs, total, err := st.SearchSubtitles(context.Background(), SearchParams{
			ImdbID: "tt0133093", Language: "english", Limit: 10, Offset: 20,
		})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if total != 25 {
			t.Errorf("total = %d, want the exact match count 25", total)
		}
		if len(subs) != 5 {
			t.Errorf("page holds %d, want the remaining 5", len(subs))
		}
	})
}

func TestConformance_ResultsAreOrderedByDownloads(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st Store) {
		ctx := context.Background()
		darkKnight(t, st)

		for range 3 {
			if err := st.IncrementDownloads(ctx, "f103"); err != nil {
				t.Fatalf("IncrementDownloads: %v", err)
			}
		}

		subs, _, err := st.SearchSubtitles(ctx, SearchParams{ImdbID: "tt0468569", Language: "english", Limit: 50})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if subs[0].ID != "f103" || subs[0].Downloads != 3 {
			t.Errorf("first result %s with %d downloads, want f103 with 3", subs[0].ID, subs[0].Downloads)
		}
	})
}

func TestConformance_GetSubtitle(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st Store) {
		darkKnight(t, st)
		ctx := context.Background()

		sub, err := st.GetSubtitle(ctx, "f101")
		if err != nil {
			t.Fatalf("GetSubtitle: %v", err)
		}
		if sub.Title != "The Dark Knight" || sub.Language != "english" || sub.ContentKey == "" {
			t.Errorf("got %+v, want the upload's metadata joined onto the file", sub)
		}

		if _, err := st.GetSubtitle(ctx, "nope"); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("GetSubtitle of an unknown id returned %v, want sql.ErrNoRows", err)
		}
	})
}

// An upload with no stored files is in the catalogue but not available, and must
// not be advertised as a language a client can ask for.
func TestConformance_ListLanguagesCountsOnlyStoredFiles(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st Store) {
		seed(t, st,
			[]Upload{
				upload("601", "arrival", "Arrival", "tt2543164", "english"),
				upload("602", "arrival", "Arrival", "tt2543164", "danish"),
				upload("603", "arrival", "Arrival", "tt2543164", "thai"), // never stored
			},
			[]IngestFile{
				file("f601", "601", "Arrival.srt", hash(30)),
				file("f602", "602", "Arrival.dan.srt", hash(31)),
			},
		)

		langs, err := st.ListLanguages(context.Background())
		if err != nil {
			t.Fatalf("ListLanguages: %v", err)
		}
		for _, l := range langs {
			if l.Language == "thai" {
				t.Error("thai has no stored files and must not be listed")
			}
		}
		if len(langs) != 2 {
			t.Errorf("got %d languages, want 2: %+v", len(langs), langs)
		}
	})
}

func TestConformance_HasIMDBIDs(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st Store) {
		ctx := context.Background()

		has, err := st.HasIMDBIDs(ctx)
		if err != nil || has {
			t.Errorf("empty catalogue: has=%v err=%v, want false", has, err)
		}

		darkKnight(t, st)
		has, err = st.HasIMDBIDs(ctx)
		if err != nil || !has {
			t.Errorf("loaded catalogue: has=%v err=%v, want true", has, err)
		}
	})
}

// Re-running an import must not duplicate anything and must not renumber
// anything: the id is the download URL an operator's Bazarr already holds.
func TestConformance_IngestIsIdempotent(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st Store) {
		ctx := context.Background()
		darkKnight(t, st)

		// The same content again, offered under a different id and a corrected
		// file name.
		again := file("f101-new", "101", "The.Dark.Knight.2008.1080p.srt", hash(1))
		if err := st.IngestFiles(ctx, []IngestFile{again}); err != nil {
			t.Fatalf("re-ingest: %v", err)
		}

		files, err := st.FilesByUpload(ctx, []string{"101"})
		if err != nil {
			t.Fatalf("FilesByUpload: %v", err)
		}
		if len(files["101"]) != 1 {
			t.Fatalf("upload 101 has %d files, want 1", len(files["101"]))
		}
		got := files["101"][0]
		if got.ID != "f101" {
			t.Errorf("file id changed to %q; the download URL must survive a re-import", got.ID)
		}
		if got.Filename != "The.Dark.Knight.2008.1080p.srt" {
			t.Errorf("filename = %q, want the re-imported one", got.Filename)
		}
	})
}

func TestConformance_UpsertUploadsUpdatesInPlace(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st Store) {
		ctx := context.Background()
		darkKnight(t, st)

		u := upload("101", "the-dark-knight", "The Dark Knight", "tt0468569", "english", withYear(2008))
		u.Author = "someone"
		u.Comment = "resynced"
		if err := st.UpsertUploads(ctx, []Upload{u}); err != nil {
			t.Fatalf("UpsertUploads: %v", err)
		}

		sub, err := st.GetSubtitle(ctx, "f101")
		if err != nil {
			t.Fatalf("GetSubtitle: %v", err)
		}
		if sub.Author != "someone" || sub.Comment != "resynced" {
			t.Errorf("upload was not updated in place: %+v", sub)
		}

		count, err := st.CountUploads(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if count != 6 {
			t.Errorf("CountUploads = %d, want 6 (nothing duplicated)", count)
		}
	})
}

func TestConformance_UploadsByPath(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st Store) {
		darkKnight(t, st)

		want := "Subscene Files DB/the-dark-knight/101.zip"
		found, err := st.UploadsByPath(context.Background(), []string{want, "Subscene Files DB/nothing/0.zip"})
		if err != nil {
			t.Fatalf("UploadsByPath: %v", err)
		}
		if len(found) != 1 {
			t.Fatalf("got %d uploads, want 1", len(found))
		}
		if found[want].ID != "101" {
			t.Errorf("got %+v, want upload 101", found[want])
		}
	})
}

func TestConformance_CatalogueLanguages(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st Store) {
		darkKnight(t, st)

		langs, err := st.CatalogueLanguages(context.Background())
		if err != nil {
			t.Fatalf("CatalogueLanguages: %v", err)
		}
		if len(langs) != 2 {
			t.Errorf("got %v, want english and danish", langs)
		}
	})
}

// Pruning removes the rows for languages an operator does not keep, and reports
// the storage objects that nothing points at any more — but never an object that
// a surviving row still shares.
func TestConformance_PruneByLanguage(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st Store) {
		ctx := context.Background()
		shared := hash(42)
		seed(t, st,
			[]Upload{
				upload("701", "solaris", "Solaris", "tt0069293", "english"),
				upload("702", "solaris", "Solaris", "tt0069293", "thai"),
				upload("703", "solaris", "Solaris", "tt0069293", "thai"),
			},
			[]IngestFile{
				// 701 and 702 hold byte-identical subtitles: one storage object,
				// two rows. Pruning thai must not delete the object English uses.
				{ID: "f701", UploadID: "701", Filename: "a.srt", Format: "srt", ContentHash: shared,
					ContentKey: "content/shared.srt", Size: 100},
				{ID: "f702", UploadID: "702", Filename: "b.srt", Format: "srt", ContentHash: shared,
					ContentKey: "content/shared.srt", Size: 100},
				file("f703", "703", "c.srt", hash(43)),
			},
		)

		rows, bytes, err := st.PruneScope(ctx, []string{"english"})
		if err != nil {
			t.Fatalf("PruneScope: %v", err)
		}
		if rows != 2 || bytes == 0 {
			t.Errorf("PruneScope = %d rows, %d bytes; want 2 rows and a non-zero size", rows, bytes)
		}

		var deleted int
		var orphans []string
		for {
			batch, err := st.PruneLanguages(ctx, []string{"english"}, 10)
			if err != nil {
				t.Fatalf("PruneLanguages: %v", err)
			}
			if batch.Deleted == 0 {
				break
			}
			deleted += batch.Deleted
			orphans = append(orphans, batch.OrphanKeys...)
		}

		if deleted != 2 {
			t.Errorf("deleted %d rows, want 2", deleted)
		}
		if len(orphans) != 1 || !strings.Contains(orphans[0], hash(43)) {
			t.Errorf("orphans = %v, want only the object thai alone used", orphans)
		}

		if _, err := st.GetSubtitle(ctx, "f701"); err != nil {
			t.Errorf("the English file was removed by a thai prune: %v", err)
		}
	})
}

func TestConformance_PruneWithoutLanguagesDeletesNothing(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st Store) {
		ctx := context.Background()
		darkKnight(t, st)

		batch, err := st.PruneLanguages(ctx, nil, 100)
		if err != nil {
			t.Fatalf("PruneLanguages: %v", err)
		}
		if batch.Deleted != 0 {
			t.Errorf("an empty whitelist deleted %d rows; it must delete nothing", batch.Deleted)
		}
	})
}

// Optimize runs on every start. It must be safe on an empty database, must build
// a title index that was never built, and must not undo one that is current.
func TestConformance_OptimizeIsSafeAtAnyTime(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st Store) {
		ctx := context.Background()

		if err := st.Optimize(ctx); err != nil {
			t.Fatalf("Optimize on an empty database: %v", err)
		}

		if err := st.UpsertUploads(ctx, []Upload{
			upload("801", "the-thing", "The Thing", "tt0084787", "english"),
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.IngestFiles(ctx, []IngestFile{file("f801", "801", "The.Thing.srt", hash(50))}); err != nil {
			t.Fatal(err)
		}

		// No Reindex: Optimize has to notice the index is missing.
		if err := st.Optimize(ctx); err != nil {
			t.Fatalf("Optimize: %v", err)
		}
		_, total, err := st.SearchSubtitles(ctx, SearchParams{Query: "The Thing", Language: "english", Limit: 10})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if total != 1 {
			t.Errorf("total = %d, want 1 — Optimize should have built the title index", total)
		}

		if err := st.Optimize(ctx); err != nil {
			t.Fatalf("second Optimize: %v", err)
		}
	})
}
