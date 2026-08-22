package ingest_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"

	"github.com/slimcdk/subsarr/internal/archive"
	"github.com/slimcdk/subsarr/internal/ingest"
	"github.com/slimcdk/subsarr/internal/storage"
	"github.com/slimcdk/subsarr/internal/store"
	"github.com/slimcdk/subsarr/internal/storetest"
)

// The importer is tested at the seam below the CLI and above the store: a
// synthetic archive plus a catalogue go in, and what comes out is asserted the
// way a consumer sees it — through search, and through the objects in storage.
// A 7z cannot be written from Go, so this is the highest seam available.

const catalogueDump = "CREATE TABLE `all_subs` (\n" +
	"  `id` int(11) NOT NULL,\n" +
	"  `title` varchar(255),\n" +
	"  `imdb` varchar(20),\n" +
	"  `language` varchar(64),\n" +
	"  `releases` text,\n" +
	"  `author` varchar(128),\n" +
	"  `comment` text,\n" +
	"  `date` datetime,\n" +
	"  `file_path` varchar(512)\n" +
	");\n" +
	"INSERT INTO `all_subs` VALUES " +
	"(1001,'The Dark Knight','tt0468569','English','TDK.720p.BluRay','someone','',	'2008-07-20 13:45:00','db/the-dark-knight/the-dark-knight_english-1001.zip')," +
	"(1002,'The Dark Knight','tt0468569','Danish',NULL,'nogen',NULL,'2008-08-01 10:00:00','db/the-dark-knight/the-dark-knight_danish-1002.zip')," +
	"(1003,'Breaking Bad Second Season','tt0903747','English','Breaking.Bad.S02E05.720p','someone',NULL,'2009-05-01 10:00:00','db/breaking-bad-second-season/breaking-bad-second-season_english-1003.srt')," +
	"(1004,'The Dark Knight','tt0468569','Thai',NULL,NULL,NULL,NULL,'db/the-dark-knight/the-dark-knight_thai-1004.zip');\n"

const srtA = "1\n00:00:01,000 --> 00:00:02,000\nOne\n"
const srtB = "1\n00:00:03,000 --> 00:00:04,000\nTwo\n"

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(content))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fixture is the archive most cases use: a normal zip, a zip with two subtitles,
// a raw .srt, a truncated download and an error body saved under a .zip name.
func fixture(t *testing.T) archive.MemorySource {
	t.Helper()
	return archive.MemorySource{
		{Path: "db/the-dark-knight/the-dark-knight_english-1001.zip",
			Content: zipOf(t, map[string]string{"The.Dark.Knight.srt": srtA})},
		{Path: "db/the-dark-knight/the-dark-knight_danish-1002.zip",
			Content: zipOf(t, map[string]string{"S01E01.srt": srtA, "S01E02.srt": srtB})},
		{Path: "db/breaking-bad-second-season/breaking-bad-second-season_english-1003.srt",
			Content: []byte(srtB)},
		{Path: "db/the-dark-knight/the-dark-knight_thai-1004.zip",
			Content: []byte("PK\x03\x04truncated")},
		{Path: "db/nothing/nothing_english-1005.zip",
			Content: []byte(`{"error":"not found"}`)},
	}
}

type harness struct {
	st      store.Store
	storage *storage.Filesystem
	root    string
}

func newHarness(t *testing.T) harness {
	t.Helper()
	root := t.TempDir()
	return harness{st: storetest.SQLite(t), storage: storage.NewFilesystem(root), root: root}
}

func (h harness) run(t *testing.T, src archive.Source, opts ingest.Options) ingest.Stats {
	t.Helper()
	ctx := context.Background()

	in := ingest.New(h.st, h.storage, opts)
	if _, err := in.LoadCatalogue(ctx, strings.NewReader(catalogueDump)); err != nil {
		t.Fatalf("LoadCatalogue: %v", err)
	}
	if err := in.Run(ctx, src); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !opts.DryRun {
		if err := h.st.Reindex(ctx); err != nil {
			t.Fatalf("Reindex: %v", err)
		}
	}
	return in.Stats()
}

func (h harness) search(t *testing.T, p store.SearchParams) ([]store.Subtitle, int) {
	t.Helper()
	if p.Limit == 0 {
		p.Limit = 50
	}
	subs, total, err := h.st.SearchSubtitles(context.Background(), p)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	return subs, total
}

func TestIngest_MakesTheCatalogueSearchable(t *testing.T) {
	h := newHarness(t)
	stats := h.run(t, fixture(t), ingest.Options{Batch: 2})

	if stats.CatalogueRows != 4 {
		t.Errorf("catalogue rows = %d, want 4", stats.CatalogueRows)
	}

	subs, total := h.search(t, store.SearchParams{ImdbID: "tt0468569", Language: "english"})
	if total != 1 {
		t.Fatalf("total = %d, want 1", total)
	}

	got := subs[0]
	if got.Title != "The Dark Knight" {
		t.Errorf("title = %q — the metadata comes from the catalogue, not the file name", got.Title)
	}
	if got.ImdbID != "tt0468569" {
		t.Errorf("imdb = %q", got.ImdbID)
	}
	if got.Author != "someone" {
		t.Errorf("author = %q", got.Author)
	}
	if got.Releases != `["TDK.720p.BluRay"]` {
		t.Errorf("releases = %q", got.Releases)
	}
	if got.UploadedAt != "2008-07-20T13:45:00Z" {
		t.Errorf("uploaded_at = %q", got.UploadedAt)
	}
	if got.Filename != "The.Dark.Knight.srt" {
		t.Errorf("filename = %q", got.Filename)
	}
}

// A zip holding one subtitle per episode has to become one result per episode.
func TestIngest_OneResultPerSubtitleInAZip(t *testing.T) {
	h := newHarness(t)
	h.run(t, fixture(t), ingest.Options{})

	_, total := h.search(t, store.SearchParams{ImdbID: "tt0468569", Language: "danish"})
	if total != 2 {
		t.Errorf("total = %d, want 2 — a zip with two subtitles is two results", total)
	}
}

// About four per cent of the archive is not a zip. Those entries were skipped
// entirely before; they have to be searchable now.
func TestIngest_RawSubtitleFiles(t *testing.T) {
	h := newHarness(t)
	h.run(t, fixture(t), ingest.Options{})

	subs, total := h.search(t, store.SearchParams{ImdbID: "tt0903747", Language: "english"})
	if total != 1 {
		t.Fatalf("total = %d, want 1 — a raw .srt is a subtitle", total)
	}
	if subs[0].Format != "srt" {
		t.Errorf("format = %q", subs[0].Format)
	}
}

// An entry that holds no usable subtitle must produce no row: the catalogue still
// records that Subscene had it, but the API must never advertise a download that
// answers 404.
func TestIngest_UnusableEntriesAreCountedAndExcluded(t *testing.T) {
	h := newHarness(t)
	stats := h.run(t, fixture(t), ingest.Options{})

	if stats.Truncated != 1 {
		t.Errorf("truncated = %d, want 1", stats.Truncated)
	}
	if stats.ErrorBodies != 1 {
		t.Errorf("error bodies = %d, want 1", stats.ErrorBodies)
	}

	if _, total := h.search(t, store.SearchParams{Language: "thai"}); total != 0 {
		t.Errorf("the truncated download produced %d results, want none", total)
	}

	// The catalogue still knows about it.
	langs, err := h.st.CatalogueLanguages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !contains(langs, "thai") {
		t.Errorf("catalogue languages = %v, want thai to still be recorded", langs)
	}
}

// Identical subtitles uploaded under several ids must occupy storage once.
func TestIngest_DeduplicatesByContent(t *testing.T) {
	h := newHarness(t)
	src := archive.MemorySource{
		{Path: "db/the-dark-knight/the-dark-knight_english-1001.zip",
			Content: zipOf(t, map[string]string{"a.srt": srtA})},
		{Path: "db/the-dark-knight/the-dark-knight_danish-1002.zip",
			Content: zipOf(t, map[string]string{"b.srt": srtA})}, // the same bytes
	}
	stats := h.run(t, src, ingest.Options{})

	if stats.Files != 2 {
		t.Errorf("files = %d, want 2 rows", stats.Files)
	}
	if stats.Stored != 1 {
		t.Errorf("stored = %d, want 1 object for two rows of identical content", stats.Stored)
	}
}

// Re-running an import must not duplicate rows or stored files, and must not
// renumber anything: the file id is the download URL Bazarr already holds.
func TestIngest_IsIdempotent(t *testing.T) {
	h := newHarness(t)
	h.run(t, fixture(t), ingest.Options{})

	before, total := h.search(t, store.SearchParams{ImdbID: "tt0468569", Language: "english"})
	stats := h.run(t, fixture(t), ingest.Options{})
	after, totalAgain := h.search(t, store.SearchParams{ImdbID: "tt0468569", Language: "english"})

	if total != totalAgain {
		t.Errorf("a second import changed the result count from %d to %d", total, totalAgain)
	}
	if before[0].ID != after[0].ID {
		t.Errorf("file id changed from %s to %s on re-import", before[0].ID, after[0].ID)
	}
	if stats.Inserted != 0 {
		t.Errorf("second run inserted %d files, want 0", stats.Inserted)
	}
	if stats.Stored != 0 {
		t.Errorf("second run stored %d objects, want 0 — the content is already there", stats.Stored)
	}
	if stats.Unchanged == 0 {
		t.Error("second run reported nothing unchanged")
	}
}

// A restarted import must not decompress and hash everything a previous run
// already stored.
func TestIngest_ResumeSkipsWhatIsAlreadyStored(t *testing.T) {
	h := newHarness(t)
	h.run(t, fixture(t), ingest.Options{})

	stats := h.run(t, fixture(t), ingest.Options{Resume: true})
	if stats.Resumed == 0 {
		t.Error("a resumed run skipped nothing")
	}
	if stats.Files != 0 {
		t.Errorf("a resumed run touched %d files, want 0", stats.Files)
	}
}

func TestIngest_DryRunWritesNothing(t *testing.T) {
	h := newHarness(t)
	stats := h.run(t, fixture(t), ingest.Options{DryRun: true})

	if stats.Files == 0 {
		t.Error("a dry run should still report what it would do")
	}
	count, err := h.st.CountUploads(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("a dry run wrote %d uploads", count)
	}
	if _, total := h.search(t, store.SearchParams{ImdbID: "tt0468569"}); total != 0 {
		t.Errorf("a dry run made %d subtitles searchable", total)
	}
}

func TestIngest_LimitStopsEarly(t *testing.T) {
	h := newHarness(t)
	stats := h.run(t, fixture(t), ingest.Options{Limit: 2, Batch: 1})

	if stats.Entries != 2 {
		t.Errorf("entries = %d, want 2", stats.Entries)
	}
}

// The entries the catalogue does not cover — about 350 of two and a half million
// — still have to be importable from their file name alone.
func TestIngest_EntriesWithoutACatalogueRow(t *testing.T) {
	h := newHarness(t)
	src := archive.MemorySource{
		{Path: "db/some-other-film-1999/some-other-film-1999_HI_danish-9999.zip",
			Content: zipOf(t, map[string]string{"a.srt": srtA})},
	}
	h.run(t, src, ingest.Options{})

	subs, total := h.search(t, store.SearchParams{Query: "Some Other Film", Language: "danish"})
	if total != 1 {
		t.Fatalf("total = %d, want 1", total)
	}
	if !subs[0].HI {
		t.Error("_HI_ in the file name means hearing impaired")
	}
	if subs[0].Year != 1999 {
		t.Errorf("year = %d, want the year in the slug", subs[0].Year)
	}
	if subs[0].Title != "Some Other Film 1999" {
		t.Errorf("title = %q", subs[0].Title)
	}
}

// ─── the language whitelist ──────────────────────────────────────────────────

func TestIngest_WhitelistStoresOnlyTheListedLanguages(t *testing.T) {
	h := newHarness(t)
	stats := h.run(t, fixture(t), ingest.Options{Languages: []string{"english"}})

	// Danish and Thai, both skipped before the entry was even opened — which is
	// what makes a whitelist cheap.
	if stats.SkippedLanguage != 2 {
		t.Errorf("skipped by language = %d, want 2 (the Danish and Thai uploads)", stats.SkippedLanguage)
	}
	if stats.Truncated != 0 {
		t.Errorf("truncated = %d, want 0: the Thai entry was never opened", stats.Truncated)
	}

	if _, total := h.search(t, store.SearchParams{ImdbID: "tt0468569", Language: "danish"}); total != 0 {
		t.Errorf("danish is outside the whitelist but returned %d results", total)
	}
	if _, total := h.search(t, store.SearchParams{ImdbID: "tt0468569", Language: "english"}); total != 1 {
		t.Errorf("english is whitelisted but returned %d results", total)
	}

	// The catalogue is always complete, whatever the whitelist says.
	langs, err := h.st.CatalogueLanguages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !contains(langs, "danish") {
		t.Errorf("catalogue languages = %v, want danish to be recorded even though its files were not stored", langs)
	}

	// And what is advertised is what is available.
	available, err := h.st.ListLanguages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range available {
		if l.Language == "danish" {
			t.Error("danish has no stored files and must not be advertised")
		}
	}
}

// Changing your mind must cost one re-run and nothing else.
func TestIngest_WideningTheWhitelistAddsOnlyTheNewLanguages(t *testing.T) {
	h := newHarness(t)
	h.run(t, fixture(t), ingest.Options{Languages: []string{"english"}})

	stats := h.run(t, fixture(t), ingest.Options{Languages: []string{"english", "danish"}})

	if stats.Inserted != 2 {
		t.Errorf("inserted = %d, want the 2 Danish subtitles and nothing else", stats.Inserted)
	}
	if _, total := h.search(t, store.SearchParams{ImdbID: "tt0468569", Language: "danish"}); total != 2 {
		t.Errorf("danish returned %d results after widening the whitelist, want 2", total)
	}
	if _, total := h.search(t, store.SearchParams{ImdbID: "tt0468569", Language: "english"}); total != 1 {
		t.Errorf("english returned %d results, want the original 1", total)
	}
}

// An operator writes the language the way they know it, not the way the dump
// spells it.
func TestIngest_WhitelistAcceptsAnySpelling(t *testing.T) {
	h := newHarness(t)
	h.run(t, fixture(t), ingest.Options{Languages: []string{"English", "DANISH"}})

	if _, total := h.search(t, store.SearchParams{ImdbID: "tt0468569", Language: "danish"}); total != 2 {
		t.Errorf("danish returned %d results, want 2", total)
	}
}

// ─── storage ─────────────────────────────────────────────────────────────────

func TestIngest_StoresContentAddressed(t *testing.T) {
	h := newHarness(t)
	h.run(t, fixture(t), ingest.Options{})

	subs, _ := h.search(t, store.SearchParams{ImdbID: "tt0468569", Language: "english"})
	key := subs[0].ContentKey

	if !strings.HasPrefix(key, "content/") || !strings.HasSuffix(key, ".srt") {
		t.Errorf("content key = %q, want a content-addressed key", key)
	}
	if !strings.Contains(key, subs[0].ContentHash) {
		t.Errorf("content key %q does not carry the hash %q", key, subs[0].ContentHash)
	}

	rc, err := h.storage.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("the advertised object is not in storage: %v", err)
	}
	defer rc.Close()

	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != srtA {
		t.Errorf("stored content = %q, want the subtitle that was in the zip", body)
	}
}

func sha256Of(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// An installation upgraded from the flat model has its files under
// `subtitles/<uuid>/<name>`. The import has to move them and remove the old
// object, without changing the id the file is downloaded by.
func TestIngest_MigratesLegacyStorageKeys(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	legacyKey := "subtitles/legacy-id/the_dark_knight.srt"
	if err := h.storage.Put(ctx, legacyKey, strings.NewReader(srtA), int64(len(srtA))); err != nil {
		t.Fatal(err)
	}
	if err := h.st.UpsertUploads(ctx, []store.Upload{{
		ID: "1001", SubsceneID: "1001", Slug: "the-dark-knight", Title: "The Dark Knight",
		ImdbID: "tt0468569", Language: "english", Releases: "[]",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.IngestFiles(ctx, []store.IngestFile{{
		ID: "legacy-id", UploadID: "1001", Filename: "the_dark_knight.srt", Format: "srt",
		ContentHash: sha256Of(srtA), ContentKey: legacyKey,
	}}); err != nil {
		t.Fatal(err)
	}

	stats := h.run(t, fixture(t), ingest.Options{})

	if stats.Migrated != 1 {
		t.Errorf("migrated = %d, want 1", stats.Migrated)
	}
	subs, _ := h.search(t, store.SearchParams{ImdbID: "tt0468569", Language: "english"})
	if subs[0].ID != "legacy-id" {
		t.Errorf("file id = %q, want the legacy id preserved", subs[0].ID)
	}
	if !strings.HasPrefix(subs[0].ContentKey, "content/") {
		t.Errorf("content key = %q, want the content-addressed one", subs[0].ContentKey)
	}
	if exists, _ := h.storage.Exists(ctx, legacyKey); exists {
		t.Error("the legacy object was left behind")
	}
	if exists, _ := h.storage.Exists(ctx, subs[0].ContentKey); !exists {
		t.Error("the new object is not in storage")
	}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
