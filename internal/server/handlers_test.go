package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/slimcdk/subsarr/internal/store"
)

// ─── mock store ──────────────────────────────────────────────────────────────

type mockStore struct {
	subtitles map[string]*store.Subtitle
	languages []store.LanguageCount
	searchFn  func(store.SearchParams) ([]store.Subtitle, error)
}

func newMockStore() *mockStore {
	return &mockStore{subtitles: make(map[string]*store.Subtitle)}
}

func (m *mockStore) GetSubtitle(_ context.Context, id string) (*store.Subtitle, error) {
	s, ok := m.subtitles[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return s, nil
}

func (m *mockStore) InsertSubtitle(_ context.Context, s *store.Subtitle) (bool, error) {
	m.subtitles[s.ID] = s
	return true, nil
}

func (m *mockStore) InsertSubtitleBatch(_ context.Context, subs []*store.Subtitle) (int, int, int) {
	for _, s := range subs {
		m.subtitles[s.ID] = s
	}
	return len(subs), 0, 0
}

func (m *mockStore) IncrementDownloads(_ context.Context, id string) error {
	if s, ok := m.subtitles[id]; ok {
		s.Downloads++
	}
	return nil
}

func (m *mockStore) ListLanguages(_ context.Context) ([]store.LanguageCount, error) {
	return m.languages, nil
}

func (m *mockStore) SearchSubtitles(_ context.Context, p store.SearchParams) ([]store.Subtitle, error) {
	if m.searchFn != nil {
		return m.searchFn(p)
	}
	var results []store.Subtitle
	for _, s := range m.subtitles {
		results = append(results, *s)
	}
	return results, nil
}

// ─── mock storage ────────────────────────────────────────────────────────────

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

func (m *mockStorage) Delete(_ context.Context, key string) error {
	delete(m.files, key)
	return nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func parseJSON(t *testing.T, body io.Reader) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(body).Decode(&m); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}
	return m
}

// ─── /api/v1/info ────────────────────────────────────────────────────────────

func TestHandleInfo(t *testing.T) {
	srv := New(newMockStore(), newMockStorage())
	req := httptest.NewRequest("GET", "/api/v1/info", nil)
	w := httptest.NewRecorder()

	srv.Routes().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	body := parseJSON(t, w.Body)
	if body["name"] != "subsarr" {
		t.Errorf("name = %v, want subsarr", body["name"])
	}
	if body["version"] != "1.0.0" {
		t.Errorf("version = %v, want 1.0.0", body["version"])
	}

	features, ok := body["features"].(map[string]any)
	if !ok {
		t.Fatal("features not a map")
	}
	if features["search_by_imdb_id"] != true {
		t.Error("search_by_imdb_id should be true")
	}
}

// ─── /api/v1/languages ──────────────────────────────────────────────────────

func TestHandleLanguages(t *testing.T) {
	ms := newMockStore()
	ms.languages = []store.LanguageCount{
		{Language: "English", Count: 100},
		{Language: "French", Count: 50},
	}
	srv := New(ms, newMockStorage())

	req := httptest.NewRequest("GET", "/api/v1/languages", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	body := parseJSON(t, w.Body)
	if body["total_items"] != float64(2) {
		t.Errorf("total_items = %v, want 2", body["total_items"])
	}
	items := body["items"].([]any)
	first := items[0].(map[string]any)
	if first["name"] != "English" {
		t.Errorf("first language = %v, want English", first["name"])
	}
}

func TestHandleLanguages_Empty(t *testing.T) {
	srv := New(newMockStore(), newMockStorage())

	req := httptest.NewRequest("GET", "/api/v1/languages", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	body := parseJSON(t, w.Body)
	if body["total_items"] != float64(0) {
		t.Errorf("total_items = %v, want 0", body["total_items"])
	}
}

// ─── /api/v1/subtitles/search ────────────────────────────────────────────────

func TestHandleSearch(t *testing.T) {
	ms := newMockStore()
	ms.searchFn = func(p store.SearchParams) ([]store.Subtitle, error) {
		return []store.Subtitle{
			{ID: "s1", Title: "Test", Language: "English", Releases: `["1080p"]`, Downloads: 10},
		}, nil
	}
	srv := New(ms, newMockStorage())

	req := httptest.NewRequest("GET", "/api/v1/subtitles/search?language=English&per_page=10", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	body := parseJSON(t, w.Body)
	if body["total_items"] != float64(1) {
		t.Errorf("total_items = %v, want 1", body["total_items"])
	}
	if body["page"] != float64(1) {
		t.Errorf("page = %v, want 1", body["page"])
	}
	if body["per_page"] != float64(10) {
		t.Errorf("per_page = %v, want 10", body["per_page"])
	}

	items := body["items"].([]any)
	item := items[0].(map[string]any)
	if item["id"] != "s1" {
		t.Errorf("id = %v, want s1", item["id"])
	}
	if item["download_url"] == nil || item["download_url"] == "" {
		t.Error("download_url should be set")
	}
}

func TestHandleSearch_Pagination(t *testing.T) {
	ms := newMockStore()
	ms.searchFn = func(p store.SearchParams) ([]store.Subtitle, error) {
		if p.Limit != 5 || p.Offset != 10 {
			t.Errorf("params = limit=%d offset=%d, want limit=5 offset=10", p.Limit, p.Offset)
		}
		return nil, nil
	}
	srv := New(ms, newMockStorage())

	req := httptest.NewRequest("GET", "/api/v1/subtitles/search?page=3&per_page=5", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestHandleSearch_PerPageClamped(t *testing.T) {
	ms := newMockStore()
	ms.searchFn = func(p store.SearchParams) ([]store.Subtitle, error) {
		if p.Limit != 200 {
			t.Errorf("limit = %d, want 200 (clamped)", p.Limit)
		}
		return nil, nil
	}
	srv := New(ms, newMockStorage())

	req := httptest.NewRequest("GET", "/api/v1/subtitles/search?per_page=999", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
}

func TestHandleSearch_SeasonEpisode(t *testing.T) {
	ms := newMockStore()
	ms.searchFn = func(p store.SearchParams) ([]store.Subtitle, error) {
		if p.SeasonEp != "S02E05" {
			t.Errorf("SeasonEp = %q, want S02E05", p.SeasonEp)
		}
		return nil, nil
	}
	srv := New(ms, newMockStorage())

	req := httptest.NewRequest("GET", "/api/v1/subtitles/search?season=2&episode=5", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
}

func TestHandleSearch_SeasonOnly(t *testing.T) {
	ms := newMockStore()
	ms.searchFn = func(p store.SearchParams) ([]store.Subtitle, error) {
		if p.SeasonEp != "S03" {
			t.Errorf("SeasonEp = %q, want S03", p.SeasonEp)
		}
		return nil, nil
	}
	srv := New(ms, newMockStorage())

	req := httptest.NewRequest("GET", "/api/v1/subtitles/search?season=3", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
}

func TestHandleSearch_HI(t *testing.T) {
	ms := newMockStore()
	ms.searchFn = func(p store.SearchParams) ([]store.Subtitle, error) {
		if p.HI == nil || !*p.HI {
			t.Error("HI should be true")
		}
		return nil, nil
	}
	srv := New(ms, newMockStorage())

	req := httptest.NewRequest("GET", "/api/v1/subtitles/search?hi=true", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
}

func TestHandleSearch_ReleasesJSON(t *testing.T) {
	ms := newMockStore()
	ms.searchFn = func(p store.SearchParams) ([]store.Subtitle, error) {
		return []store.Subtitle{
			{ID: "r1", Releases: `["1080p","BluRay"]`},
		}, nil
	}
	srv := New(ms, newMockStorage())

	req := httptest.NewRequest("GET", "/api/v1/subtitles/search", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	body := parseJSON(t, w.Body)
	items := body["items"].([]any)
	item := items[0].(map[string]any)

	// Releases should be a parsed JSON array, not a string
	releases, ok := item["releases"].([]any)
	if !ok {
		t.Fatalf("releases should be an array, got %T", item["releases"])
	}
	if len(releases) != 2 {
		t.Errorf("releases len = %d, want 2", len(releases))
	}
}

// ─── /api/v1/subtitles/{id}/download ─────────────────────────────────────────

func TestHandleDownload(t *testing.T) {
	ms := newMockStore()
	ms.subtitles["dl-1"] = &store.Subtitle{
		ID:         "dl-1",
		Filename:   "movie.srt",
		ContentKey: "subtitles/dl-1/movie.srt",
	}

	stor := newMockStorage()
	stor.files["subtitles/dl-1/movie.srt"] = []byte("1\n00:00:01,000 --> 00:00:02,000\nHello")

	srv := New(ms, stor)

	req := httptest.NewRequest("GET", "/api/v1/subtitles/dl-1/download", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "movie.srt") {
		t.Errorf("Content-Disposition = %q, want filename containing movie.srt", cd)
	}
	if w.Body.Len() == 0 {
		t.Error("body should not be empty")
	}
}

func TestHandleDownload_NotFound(t *testing.T) {
	srv := New(newMockStore(), newMockStorage())

	req := httptest.NewRequest("GET", "/api/v1/subtitles/nonexistent/download", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleDownload_NoContent(t *testing.T) {
	ms := newMockStore()
	ms.subtitles["no-content"] = &store.Subtitle{
		ID:         "no-content",
		ContentKey: "", // no file stored
	}
	srv := New(ms, newMockStorage())

	req := httptest.NewRequest("GET", "/api/v1/subtitles/no-content/download", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleDownload_StorageMissing(t *testing.T) {
	ms := newMockStore()
	ms.subtitles["s-miss"] = &store.Subtitle{
		ID:         "s-miss",
		ContentKey: "subtitles/s-miss/file.srt",
	}
	// Storage has no file for this key
	srv := New(ms, newMockStorage())

	req := httptest.NewRequest("GET", "/api/v1/subtitles/s-miss/download", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// ─── 404 for unknown routes ──────────────────────────────────────────────────

func TestUnknownRoute(t *testing.T) {
	srv := New(newMockStore(), newMockStorage())

	req := httptest.NewRequest("GET", "/api/v1/nonexistent", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
