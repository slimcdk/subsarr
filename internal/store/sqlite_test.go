package store

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/slimcdk/subsarr/internal/sqlitedriver"
)

// openTestDB creates an in-memory SQLite database with the subtitles schema.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3_subsarr", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	schema := `
	CREATE TABLE subtitles (
		id          TEXT PRIMARY KEY,
		subscene_id TEXT NOT NULL,
		title       TEXT NOT NULL,
		slug        TEXT NOT NULL DEFAULT '',
		imdb_id     TEXT NOT NULL DEFAULT '',
		language    TEXT NOT NULL,
		hi          INTEGER NOT NULL DEFAULT 0,
		author      TEXT NOT NULL DEFAULT '',
		releases    TEXT NOT NULL DEFAULT '[]',
		comment     TEXT NOT NULL DEFAULT '',
		year        INTEGER NOT NULL DEFAULT 0,
		filename    TEXT NOT NULL,
		format      TEXT NOT NULL DEFAULT '',
		content_key TEXT NOT NULL DEFAULT '',
		uploaded_at TEXT NOT NULL DEFAULT '',
		downloads   INTEGER NOT NULL DEFAULT 0,
		created_at  TEXT NOT NULL DEFAULT '',
		updated_at  TEXT NOT NULL DEFAULT ''
	);
	CREATE UNIQUE INDEX idx_subtitles_subscene_file ON subtitles(slug, subscene_id, filename);
	CREATE INDEX idx_subtitles_imdb_id ON subtitles(imdb_id);
	CREATE INDEX idx_subtitles_language ON subtitles(language);
	CREATE INDEX idx_subtitles_slug ON subtitles(slug);
	CREATE INDEX idx_subtitles_title_lang ON subtitles(title, language);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func newTestStore(t *testing.T) Store {
	t.Helper()
	db := openTestDB(t)
	st, err := New(db, "sqlite")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return st
}

func makeSub(id, title, lang string) *Subtitle {
	return &Subtitle{
		ID:         id,
		SubsceneID: id,
		Title:      title,
		Slug:       "test-slug",
		ImdbID:     "tt1234567",
		Language:   lang,
		Filename:   title + ".srt",
		Format:     "srt",
		Releases:   "[]",
	}
}

// ─── InsertSubtitle + GetSubtitle ────────────────────────────────────────────

func TestInsertAndGetSubtitle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	sub := &Subtitle{
		ID:         "id-1",
		SubsceneID: "sc-1",
		Title:      "The Dark Knight",
		Slug:       "the-dark-knight",
		ImdbID:     "tt0468569",
		Language:   "English",
		HI:         true,
		Author:     "uploader",
		Releases:   `["1080p","BluRay"]`,
		Comment:    "great sub",
		Year:       2008,
		Filename:   "dark-knight.srt",
		Format:     "srt",
		ContentKey: "subtitles/id-1/dark_knight.srt",
		UploadedAt: "2024-01-15T10:00:00Z",
		Downloads:  42,
	}

	inserted, err := st.InsertSubtitle(ctx, sub)
	if err != nil {
		t.Fatalf("InsertSubtitle: %v", err)
	}
	if !inserted {
		t.Error("expected inserted=true")
	}

	got, err := st.GetSubtitle(ctx, "id-1")
	if err != nil {
		t.Fatalf("GetSubtitle: %v", err)
	}

	if got.ID != sub.ID {
		t.Errorf("ID = %q, want %q", got.ID, sub.ID)
	}
	if got.Title != sub.Title {
		t.Errorf("Title = %q, want %q", got.Title, sub.Title)
	}
	if got.ImdbID != sub.ImdbID {
		t.Errorf("ImdbID = %q, want %q", got.ImdbID, sub.ImdbID)
	}
	if got.Language != sub.Language {
		t.Errorf("Language = %q, want %q", got.Language, sub.Language)
	}
	if got.HI != true {
		t.Error("HI should be true")
	}
	if got.Year != 2008 {
		t.Errorf("Year = %d, want %d", got.Year, 2008)
	}
	if got.Downloads != 42 {
		t.Errorf("Downloads = %d, want %d", got.Downloads, 42)
	}
	if got.Releases != `["1080p","BluRay"]` {
		t.Errorf("Releases = %q, want %q", got.Releases, `["1080p","BluRay"]`)
	}
	if got.ContentKey != sub.ContentKey {
		t.Errorf("ContentKey = %q, want %q", got.ContentKey, sub.ContentKey)
	}
}

func TestGetSubtitle_NotFound(t *testing.T) {
	st := newTestStore(t)
	_, err := st.GetSubtitle(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent ID")
	}
}

// ─── InsertSubtitle duplicate handling ───────────────────────────────────────

func TestInsertSubtitle_Duplicate(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	sub := makeSub("dup-1", "Movie", "English")
	inserted, err := st.InsertSubtitle(ctx, sub)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if !inserted {
		t.Error("first insert should succeed")
	}

	// Same slug+subscene_id+filename = duplicate
	inserted, err = st.InsertSubtitle(ctx, sub)
	if err != nil {
		t.Fatalf("duplicate insert: %v", err)
	}
	if inserted {
		t.Error("duplicate insert should return inserted=false")
	}
}

// ─── InsertSubtitleBatch ─────────────────────────────────────────────────────

func TestInsertSubtitleBatch(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	subs := []*Subtitle{
		makeSub("b-1", "Movie A", "English"),
		makeSub("b-2", "Movie B", "French"),
		makeSub("b-3", "Movie C", "German"),
	}

	ins, skip, errs := st.InsertSubtitleBatch(ctx, subs)
	if ins != 3 || skip != 0 || errs != 0 {
		t.Errorf("batch = (%d, %d, %d), want (3, 0, 0)", ins, skip, errs)
	}
}

func TestInsertSubtitleBatch_Duplicates(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	sub := makeSub("bd-1", "Movie", "English")
	st.InsertSubtitle(ctx, sub)

	batch := []*Subtitle{
		sub,                                  // duplicate
		makeSub("bd-2", "Movie 2", "French"), // new
	}

	ins, skip, errs := st.InsertSubtitleBatch(ctx, batch)
	if ins != 1 || skip != 1 || errs != 0 {
		t.Errorf("batch = (%d, %d, %d), want (1, 1, 0)", ins, skip, errs)
	}
}

func TestInsertSubtitleBatch_Empty(t *testing.T) {
	st := newTestStore(t)
	ins, skip, errs := st.InsertSubtitleBatch(context.Background(), nil)
	// Empty batch should be a no-op. The current implementation tries
	// BeginTx then immediately commits with 0 rows.
	if ins != 0 || skip != 0 {
		t.Errorf("empty batch = (%d, %d, %d), want (0, 0, *)", ins, skip, errs)
	}
}

// ─── IncrementDownloads ──────────────────────────────────────────────────────

func TestIncrementDownloads(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	sub := makeSub("dl-1", "Movie", "English")
	sub.Downloads = 10
	st.InsertSubtitle(ctx, sub)

	if err := st.IncrementDownloads(ctx, "dl-1"); err != nil {
		t.Fatalf("IncrementDownloads: %v", err)
	}

	got, _ := st.GetSubtitle(ctx, "dl-1")
	if got.Downloads != 11 {
		t.Errorf("Downloads = %d, want 11", got.Downloads)
	}

	// Increment again
	st.IncrementDownloads(ctx, "dl-1")
	got, _ = st.GetSubtitle(ctx, "dl-1")
	if got.Downloads != 12 {
		t.Errorf("Downloads = %d, want 12", got.Downloads)
	}
}

// ─── ListLanguages ───────────────────────────────────────────────────────────

func TestListLanguages(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	subs := []*Subtitle{
		makeSub("l-1", "A", "English"),
		makeSub("l-2", "B", "English"),
		makeSub("l-3", "C", "French"),
		makeSub("l-4", "D", "German"),
	}
	// Each needs a unique filename to avoid uniqueness constraint
	subs[0].Filename = "a.srt"
	subs[1].Filename = "b.srt"
	subs[2].Filename = "c.srt"
	subs[3].Filename = "d.srt"
	st.InsertSubtitleBatch(ctx, subs)

	langs, err := st.ListLanguages(ctx)
	if err != nil {
		t.Fatalf("ListLanguages: %v", err)
	}
	if len(langs) != 3 {
		t.Fatalf("len(langs) = %d, want 3", len(langs))
	}

	// Should be ordered by count DESC: English(2), then French(1) and German(1)
	if langs[0].Language != "English" || langs[0].Count != 2 {
		t.Errorf("langs[0] = %+v, want English:2", langs[0])
	}
}

func TestListLanguages_Empty(t *testing.T) {
	st := newTestStore(t)
	langs, err := st.ListLanguages(context.Background())
	if err != nil {
		t.Fatalf("ListLanguages: %v", err)
	}
	if len(langs) != 0 {
		t.Errorf("len(langs) = %d, want 0", len(langs))
	}
}

// ─── SearchSubtitles ─────────────────────────────────────────────────────────

func seedSearch(t *testing.T, st Store) {
	t.Helper()
	ctx := context.Background()
	subs := []*Subtitle{
		{ID: "s1", SubsceneID: "sc1", Title: "The Dark Knight", Slug: "the-dark-knight", ImdbID: "tt0468569", Language: "English", HI: false, Filename: "dark.knight.srt", Format: "srt", Releases: `["1080p","BluRay"]`, Year: 2008, Downloads: 100},
		{ID: "s2", SubsceneID: "sc2", Title: "The Dark Knight", Slug: "the-dark-knight", ImdbID: "tt0468569", Language: "English", HI: true, Filename: "dark.knight.hi.srt", Format: "srt", Releases: `["720p"]`, Year: 2008, Downloads: 50},
		{ID: "s3", SubsceneID: "sc3", Title: "The Dark Knight", Slug: "the-dark-knight", ImdbID: "tt0468569", Language: "French", HI: false, Filename: "dark.knight.fr.srt", Format: "srt", Releases: `[]`, Year: 2008, Downloads: 30},
		{ID: "s4", SubsceneID: "sc4", Title: "Loki", Slug: "loki", ImdbID: "tt9140554", Language: "English", HI: false, Filename: "Loki.S01E03.srt", Format: "srt", Releases: `["S01E03","WEB-DL"]`, Year: 2021, Downloads: 200},
		{ID: "s5", SubsceneID: "sc5", Title: "Loki", Slug: "loki", ImdbID: "tt9140554", Language: "English", HI: false, Filename: "Loki.S02E01.srt", Format: "srt", Releases: `["S02E01","WEB-DL"]`, Year: 2023, Downloads: 150},
	}
	st.InsertSubtitleBatch(ctx, subs)
}

func TestSearch_NoFilters(t *testing.T) {
	st := newTestStore(t)
	seedSearch(t, st)

	results, err := st.SearchSubtitles(context.Background(), SearchParams{Limit: 50, Offset: 0})
	if err != nil {
		t.Fatalf("SearchSubtitles: %v", err)
	}
	if len(results) != 5 {
		t.Errorf("len = %d, want 5", len(results))
	}
	// Should be ordered by downloads DESC
	if results[0].ID != "s4" {
		t.Errorf("first result = %q, want s4 (highest downloads)", results[0].ID)
	}
}

func TestSearch_ByImdbID(t *testing.T) {
	st := newTestStore(t)
	seedSearch(t, st)

	results, _ := st.SearchSubtitles(context.Background(), SearchParams{
		ImdbID: "tt0468569", Limit: 50,
	})
	if len(results) != 3 {
		t.Errorf("len = %d, want 3 (Dark Knight only)", len(results))
	}
}

func TestSearch_ByLanguage(t *testing.T) {
	st := newTestStore(t)
	seedSearch(t, st)

	results, _ := st.SearchSubtitles(context.Background(), SearchParams{
		Language: "French", Limit: 50,
	})
	if len(results) != 1 {
		t.Errorf("len = %d, want 1", len(results))
	}
	if len(results) > 0 && results[0].Language != "French" {
		t.Errorf("Language = %q, want French", results[0].Language)
	}
}

func TestSearch_BySlug(t *testing.T) {
	st := newTestStore(t)
	seedSearch(t, st)

	results, _ := st.SearchSubtitles(context.Background(), SearchParams{
		Slug: "loki", Limit: 50,
	})
	if len(results) != 2 {
		t.Errorf("len = %d, want 2", len(results))
	}
}

func TestSearch_ByHI(t *testing.T) {
	st := newTestStore(t)
	seedSearch(t, st)

	hi := true
	results, _ := st.SearchSubtitles(context.Background(), SearchParams{
		HI: &hi, Limit: 50,
	})
	if len(results) != 1 {
		t.Errorf("len = %d, want 1", len(results))
	}
	if len(results) > 0 && !results[0].HI {
		t.Error("expected HI=true")
	}
}

func TestSearch_ByYear(t *testing.T) {
	st := newTestStore(t)
	seedSearch(t, st)

	results, _ := st.SearchSubtitles(context.Background(), SearchParams{
		Year: 2021, Limit: 50,
	})
	if len(results) != 1 {
		t.Errorf("len = %d, want 1", len(results))
	}
}

func TestSearch_ByQuery(t *testing.T) {
	st := newTestStore(t)
	seedSearch(t, st)

	results, _ := st.SearchSubtitles(context.Background(), SearchParams{
		Query: "dark", Limit: 50,
	})
	if len(results) != 3 {
		t.Errorf("len = %d, want 3 (all Dark Knight subs)", len(results))
	}
}

func TestSearch_BySeasonEpisode(t *testing.T) {
	st := newTestStore(t)
	seedSearch(t, st)

	// Season+episode in releases
	results, _ := st.SearchSubtitles(context.Background(), SearchParams{
		SeasonEp: "S01E03", Limit: 50,
	})
	if len(results) != 1 {
		t.Errorf("len = %d, want 1", len(results))
	}

	// Season only — matches both S01E03 and S02E01 via filename
	results, _ = st.SearchSubtitles(context.Background(), SearchParams{
		SeasonEp: "S02", Limit: 50,
	})
	if len(results) != 1 {
		t.Errorf("S02 len = %d, want 1", len(results))
	}
}

func TestSearch_CombinedFilters(t *testing.T) {
	st := newTestStore(t)
	seedSearch(t, st)

	results, _ := st.SearchSubtitles(context.Background(), SearchParams{
		ImdbID:   "tt0468569",
		Language: "English",
		Limit:    50,
	})
	if len(results) != 2 {
		t.Errorf("len = %d, want 2 (English Dark Knight)", len(results))
	}
}

func TestSearch_Pagination(t *testing.T) {
	st := newTestStore(t)
	seedSearch(t, st)

	page1, _ := st.SearchSubtitles(context.Background(), SearchParams{Limit: 2, Offset: 0})
	page2, _ := st.SearchSubtitles(context.Background(), SearchParams{Limit: 2, Offset: 2})
	page3, _ := st.SearchSubtitles(context.Background(), SearchParams{Limit: 2, Offset: 4})

	if len(page1) != 2 {
		t.Errorf("page1 len = %d, want 2", len(page1))
	}
	if len(page2) != 2 {
		t.Errorf("page2 len = %d, want 2", len(page2))
	}
	if len(page3) != 1 {
		t.Errorf("page3 len = %d, want 1", len(page3))
	}
}

func TestSearch_NoResults(t *testing.T) {
	st := newTestStore(t)
	seedSearch(t, st)

	results, err := st.SearchSubtitles(context.Background(), SearchParams{
		Language: "Klingon", Limit: 50,
	})
	if err != nil {
		t.Fatalf("SearchSubtitles: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len = %d, want 0", len(results))
	}
}

// ─── New factory ─────────────────────────────────────────────────────────────

func TestNew_UnsupportedDriver(t *testing.T) {
	_, err := New(nil, "oracle")
	if err == nil {
		t.Error("expected error for unsupported driver")
	}
}
