package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/slimcdk/subsarr/internal/storage"
	"github.com/slimcdk/subsarr/internal/store"
)

// ─── mocks ───────────────────────────────────────────────────────────────────

// mockStore implements only what the handlers use. The embedded interface is
// nil, so a handler that starts calling something else fails loudly instead of
// being silently mocked. The end-to-end suite covers the real store.
type mockStore struct {
	store.Store

	subtitles map[string]*store.Subtitle
	languages []store.LanguageCount
	hasIMDB   bool
	searchFn  func(store.SearchParams) ([]store.Subtitle, int, error)

	languageCalls atomic.Int32
	lastParams    store.SearchParams
}

func newMockStore() *mockStore {
	return &mockStore{subtitles: make(map[string]*store.Subtitle), hasIMDB: true}
}

func (m *mockStore) GetSubtitle(_ context.Context, id string) (*store.Subtitle, error) {
	s, ok := m.subtitles[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return s, nil
}

func (m *mockStore) IncrementDownloads(_ context.Context, id string) error {
	if s, ok := m.subtitles[id]; ok {
		s.Downloads++
	}
	return nil
}

func (m *mockStore) ListLanguages(context.Context) ([]store.LanguageCount, error) {
	m.languageCalls.Add(1)
	return m.languages, nil
}

func (m *mockStore) HasIMDBIDs(context.Context) (bool, error) { return m.hasIMDB, nil }

func (m *mockStore) SearchSubtitles(_ context.Context, p store.SearchParams) ([]store.Subtitle, int, error) {
	m.lastParams = p
	if m.searchFn != nil {
		return m.searchFn(p)
	}
	var results []store.Subtitle
	for _, s := range m.subtitles {
		results = append(results, *s)
	}
	return results, len(results), nil
}

type mockStorage struct {
	files map[string][]byte
}

func newMockStorage() *mockStorage {
	return &mockStorage{files: make(map[string][]byte)}
}

func (m *mockStorage) Put(_ context.Context, key string, r io.Reader, _ int64) error {
	data, _ := io.ReadAll(r)
	m.files[key] = data
	return nil
}

func (m *mockStorage) Get(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := m.files[key]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return io.NopCloser(strings.NewReader(string(data))), nil
}

func (m *mockStorage) Exists(_ context.Context, key string) (bool, error) {
	_, ok := m.files[key]
	return ok, nil
}

func (m *mockStorage) Delete(_ context.Context, key string) error {
	delete(m.files, key)
	return nil
}

var _ storage.Store = (*mockStorage)(nil)

// ─── helpers ─────────────────────────────────────────────────────────────────

func get(t *testing.T, srv *Server, url string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
	return w
}

func parseJSON(t *testing.T, body io.Reader) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(body).Decode(&m); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}
	return m
}

func newServer(st store.Store, stor storage.Store) *Server {
	return New(st, stor, "1.1.0-test")
}

// ─── /api/v1/info ────────────────────────────────────────────────────────────

func TestHandleInfo(t *testing.T) {
	ms := newMockStore()
	ms.languages = []store.LanguageCount{{Language: "english", Count: 1}}
	w := get(t, newServer(ms, newMockStorage()), "/api/v1/info")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := parseJSON(t, w.Body)
	if body["name"] != "subsarr" {
		t.Errorf("name = %v, want subsarr", body["name"])
	}
	if body["version"] != "1.1.0-test" {
		t.Errorf("version = %v, want the build version", body["version"])
	}
	features := body["features"].(map[string]any)
	if features["search_by_imdb_id"] != true {
		t.Error("search_by_imdb_id should be true when the catalogue has IMDB ids")
	}
}

// An installation whose data has no IMDB ids must say so, or every client will
// keep asking for something that cannot be there.
func TestHandleInfo_ReportsFeaturesFromTheData(t *testing.T) {
	ms := newMockStore()
	ms.hasIMDB = false
	w := get(t, newServer(ms, newMockStorage()), "/api/v1/info")

	features := parseJSON(t, w.Body)["features"].(map[string]any)
	if features["search_by_imdb_id"] != false {
		t.Error("search_by_imdb_id should be false when no upload carries one")
	}
	if features["language_filter"] != false {
		t.Error("language_filter should be false when nothing is stored")
	}
}

// ─── /api/v1/languages ───────────────────────────────────────────────────────

func TestHandleLanguages(t *testing.T) {
	ms := newMockStore()
	ms.languages = []store.LanguageCount{
		{Language: "english", Count: 100},
		{Language: "danish", Count: 20},
	}
	w := get(t, newServer(ms, newMockStorage()), "/api/v1/languages")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := parseJSON(t, w.Body)
	if body["total_items"].(float64) != 2 {
		t.Errorf("total_items = %v, want the number of languages", body["total_items"])
	}
	items := body["items"].([]any)
	first := items[0].(map[string]any)
	if first["name"] != "english" || first["count"].(float64) != 100 {
		t.Errorf("first item = %v", first)
	}
}

func TestHandleLanguages_EmptyIsAnArray(t *testing.T) {
	w := get(t, newServer(newMockStore(), newMockStorage()), "/api/v1/languages")

	if !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Errorf("body = %s, want items to be an empty array, never null", w.Body.String())
	}
}

// The language list aggregates every row in the database. Serving it from cache
// is the difference between a millisecond and a twenty-second wait.
func TestHandleLanguages_ServedFromCache(t *testing.T) {
	ms := newMockStore()
	ms.languages = []store.LanguageCount{{Language: "danish", Count: 3}}
	srv := newServer(ms, newMockStorage())

	for i := range 3 {
		if w := get(t, srv, "/api/v1/languages"); w.Code != http.StatusOK {
			t.Fatalf("call %d: status = %d", i, w.Code)
		}
	}
	if got := ms.languageCalls.Load(); got != 1 {
		t.Errorf("store hit %d times, want 1", got)
	}
}

func TestWarm_LoadsTheCatalogueBeforeTheFirstRequest(t *testing.T) {
	ms := newMockStore()
	srv := newServer(ms, newMockStorage())

	if err := srv.Warm(context.Background()); err != nil {
		t.Fatalf("Warm: %v", err)
	}
	get(t, srv, "/api/v1/languages")

	if got := ms.languageCalls.Load(); got != 1 {
		t.Errorf("store hit %d times, want 1 — the request should have been served from the warmed cache", got)
	}
}

// ─── /api/v1/subtitles/search ────────────────────────────────────────────────

func TestHandleSearch(t *testing.T) {
	ms := newMockStore()
	ms.subtitles["abc"] = &store.Subtitle{
		ID: "abc", Title: "The Dark Knight", Language: "english",
		Filename: "tdk.srt", Format: "srt", Releases: `["TDK.720p"]`,
	}
	w := get(t, newServer(ms, newMockStorage()), "/api/v1/subtitles/search?query=dark")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := parseJSON(t, w.Body)
	items := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	item := items[0].(map[string]any)
	if item["download_url"] != "http://example.com/api/v1/subtitles/abc/download" {
		t.Errorf("download_url = %v", item["download_url"])
	}
	if releases := item["releases"].([]any); len(releases) != 1 || releases[0] != "TDK.720p" {
		t.Errorf("releases = %v, want the uploader's list", item["releases"])
	}
}

func TestHandleSearch_EmptyResultIsAnArray(t *testing.T) {
	ms := newMockStore()
	ms.searchFn = func(store.SearchParams) ([]store.Subtitle, int, error) { return nil, 0, nil }
	w := get(t, newServer(ms, newMockStorage()), "/api/v1/subtitles/search?query=nothing")

	if !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Errorf("body = %s, want items to be an empty array, never null", w.Body.String())
	}
}

// `releases` is documented as an array. A row holding something else must not
// change the shape of the response Bazarr parses.
func TestHandleSearch_ReleasesIsAlwaysAnArray(t *testing.T) {
	for _, stored := range []string{"", "[]", "not json", `{"a":1}`, `["x"]`} {
		ms := newMockStore()
		ms.subtitles["abc"] = &store.Subtitle{ID: "abc", Releases: stored}
		w := get(t, newServer(ms, newMockStorage()), "/api/v1/subtitles/search")

		item := parseJSON(t, w.Body)["items"].([]any)[0].(map[string]any)
		if _, ok := item["releases"].([]any); !ok {
			t.Errorf("stored %q produced releases = %#v, want an array", stored, item["releases"])
		}
	}
}

func TestHandleSearch_Pagination(t *testing.T) {
	ms := newMockStore()
	ms.searchFn = func(p store.SearchParams) ([]store.Subtitle, int, error) {
		return nil, 137, nil
	}
	w := get(t, newServer(ms, newMockStorage()), "/api/v1/subtitles/search?page=3&per_page=25")

	body := parseJSON(t, w.Body)
	if body["page"].(float64) != 3 || body["per_page"].(float64) != 25 {
		t.Errorf("page/per_page = %v/%v", body["page"], body["per_page"])
	}
	if body["total_items"].(float64) != 137 {
		t.Errorf("total_items = %v, want the exact match count", body["total_items"])
	}
	if ms.lastParams.Offset != 50 || ms.lastParams.Limit != 25 {
		t.Errorf("offset/limit = %d/%d, want 50/25", ms.lastParams.Offset, ms.lastParams.Limit)
	}
}

func TestHandleSearch_PerPageIsClampedNotRefused(t *testing.T) {
	ms := newMockStore()
	w := get(t, newServer(ms, newMockStorage()), "/api/v1/subtitles/search?per_page=1000")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ms.lastParams.Limit != 200 {
		t.Errorf("limit = %d, want it clamped to 200", ms.lastParams.Limit)
	}
}

func TestHandleSearch_SeasonAndEpisode(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{"season=2&episode=5", "S02E05"},
		{"season=2", "S02"},
		{"season=12&episode=345", "S12E345"},
		// An episode on its own matches numbers that mean something else.
		{"episode=5", ""},
		{"", ""},
	}
	for _, tc := range tests {
		ms := newMockStore()
		w := get(t, newServer(ms, newMockStorage()), "/api/v1/subtitles/search?"+tc.query)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", tc.query, w.Code)
		}
		if ms.lastParams.SeasonEp != tc.want {
			t.Errorf("%s produced %q, want %q", tc.query, ms.lastParams.SeasonEp, tc.want)
		}
	}
}

func TestHandleSearch_HearingImpairedIsTriState(t *testing.T) {
	tests := []struct {
		query string
		want  *bool
	}{
		{"", nil},
		{"hi=true", ptr(true)},
		{"hi=false", ptr(false)},
	}
	for _, tc := range tests {
		ms := newMockStore()
		get(t, newServer(ms, newMockStorage()), "/api/v1/subtitles/search?"+tc.query)

		got := ms.lastParams.HI
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("%q set hi to %v, want it unset", tc.query, *got)
		case tc.want != nil && (got == nil || *got != *tc.want):
			t.Errorf("%q produced %v, want %v", tc.query, got, *tc.want)
		}
	}
}

// The requested language has to be canonicalised the same way the stored one is,
// or a spelling Bazarr uses reaches nothing.
func TestHandleSearch_CanonicalisesLanguage(t *testing.T) {
	ms := newMockStore()
	get(t, newServer(ms, newMockStorage()), "/api/v1/subtitles/search?language=Brazilian%20Portuguese")

	if ms.lastParams.Language != "brazillian-portuguese" {
		t.Errorf("language = %q, want the canonical spelling", ms.lastParams.Language)
	}
}

// Bazarr constructs its queries; a 400 aborts the whole search. Only input that
// cannot be read at all is refused.
func TestHandleSearch_RejectsOnlyMalformedInput(t *testing.T) {
	bad := []string{
		"page=abc", "page=0", "page=-1",
		"per_page=abc", "per_page=0",
		"season=abc", "season=-1",
		"episode=abc", "episode=-1",
		"year=abc", "year=-1",
		"hi=yes", "hi=1",
	}
	for _, query := range bad {
		w := get(t, newServer(newMockStore(), newMockStorage()), "/api/v1/subtitles/search?"+query)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%q returned %d, want 400", query, w.Code)
		}
	}

	fine := []string{
		"imdb_id=not-an-imdb-id", "imdb_id=tt123", "per_page=100000",
		"year=0", "season=0", "episode=0", "query=", "language=klingon",
		"page=1&per_page=200",
	}
	for _, query := range fine {
		w := get(t, newServer(newMockStore(), newMockStorage()), "/api/v1/subtitles/search?"+query)
		if w.Code != http.StatusOK {
			t.Errorf("%q returned %d, want 200: %s", query, w.Code, w.Body.String())
		}
	}
}

// A catalogue with no IMDB ids cannot answer an IMDB search, so it should not
// try: the miss is the common case and must be cheap.
func TestHandleSearch_IMDBSearchShortCircuitsWithoutIMDBIDs(t *testing.T) {
	ms := newMockStore()
	ms.hasIMDB = false
	searched := false
	ms.searchFn = func(store.SearchParams) ([]store.Subtitle, int, error) {
		searched = true
		return nil, 0, nil
	}

	w := get(t, newServer(ms, newMockStorage()), "/api/v1/subtitles/search?imdb_id=tt0468569")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if searched {
		t.Error("the store was queried for IMDB ids that cannot be there")
	}
	if parseJSON(t, w.Body)["total_items"].(float64) != 0 {
		t.Error("total_items should be 0")
	}
}

// ─── /api/v1/subtitles/{id}/download ─────────────────────────────────────────

func TestHandleDownload(t *testing.T) {
	ms := newMockStore()
	ms.subtitles["abc"] = &store.Subtitle{ID: "abc", Filename: "tdk.srt", ContentKey: "content/ab/cd/abcd.srt"}
	stor := newMockStorage()
	stor.files["content/ab/cd/abcd.srt"] = []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n")

	w := get(t, newServer(ms, stor), "/api/v1/subtitles/abc/download")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Hello") {
		t.Errorf("body = %q, want the stored subtitle", w.Body.String())
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, `filename="tdk.srt"`) {
		t.Errorf("Content-Disposition = %q", cd)
	}
}

// A file name is whatever the uploader typed; it must not be able to break out
// of the header it is quoted in.
func TestHandleDownload_SanitisesFilename(t *testing.T) {
	ms := newMockStore()
	ms.subtitles["abc"] = &store.Subtitle{
		ID: "abc", Filename: "evil\";\r\nX-Injected: 1\".srt", ContentKey: "k",
	}
	stor := newMockStorage()
	stor.files["k"] = []byte("x")

	w := get(t, newServer(ms, stor), "/api/v1/subtitles/abc/download")

	if got := w.Header().Get("X-Injected"); got != "" {
		t.Errorf("header injection succeeded: X-Injected = %q", got)
	}
	if cd := w.Header().Get("Content-Disposition"); strings.ContainsAny(cd, "\r\n") {
		t.Errorf("Content-Disposition contains a line break: %q", cd)
	}
}

func TestHandleDownload_NotFound(t *testing.T) {
	w := get(t, newServer(newMockStore(), newMockStorage()), "/api/v1/subtitles/missing/download")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleDownload_StorageMissing(t *testing.T) {
	ms := newMockStore()
	ms.subtitles["abc"] = &store.Subtitle{ID: "abc", ContentKey: "content/gone.srt"}
	w := get(t, newServer(ms, newMockStorage()), "/api/v1/subtitles/abc/download")

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// ─── routing ─────────────────────────────────────────────────────────────────

func TestHandleOpenAPI(t *testing.T) {
	w := get(t, newServer(newMockStore(), newMockStorage()), "/api/v1/openapi.yaml")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "openapi:") {
		t.Errorf("body does not look like an OpenAPI document: %.80s", w.Body.String())
	}
}

func TestUnknownRoute(t *testing.T) {
	w := get(t, newServer(newMockStore(), newMockStorage()), "/api/v1/nope")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func ptr[T any](v T) *T { return &v }
