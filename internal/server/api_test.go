package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/slimcdk/subsarr/internal/server"
	"github.com/slimcdk/subsarr/internal/storage"
	"github.com/slimcdk/subsarr/internal/store"
	"github.com/slimcdk/subsarr/internal/storetest"
)

// The product seam: the HTTP API over a real, migrated database and real
// storage, exercised the way Bazarr exercises it. Everything below the handler —
// the SQL, the dialect, the index — is included, and on CI this runs against
// PostgreSQL and MariaDB as well as SQLite.

const subtitleBody = "1\n00:00:01,000 --> 00:00:02,000\nHello\n"

type api struct {
	handler http.Handler
	store   store.Store
	storage storage.Store
}

func newAPI(t *testing.T, st store.Store) api {
	t.Helper()
	ctx := context.Background()
	stor := storage.NewFilesystem(t.TempDir())

	uploads := []store.Upload{
		{ID: "1001", SubsceneID: "1001", Slug: "the-dark-knight", Title: "The Dark Knight",
			ImdbID: "tt0468569", Language: "english", Year: 2008, Author: "someone",
			Releases: `["TDK.720p.BluRay","TDK.1080p"]`, UploadedAt: "2008-07-20T13:45:00Z"},
		{ID: "1002", SubsceneID: "1002", Slug: "the-dark-knight", Title: "The Dark Knight",
			ImdbID: "tt0468569", Language: "english", Year: 2008, HI: true, Releases: "[]"},
		{ID: "1003", SubsceneID: "1003", Slug: "the-dark-knight", Title: "The Dark Knight",
			ImdbID: "tt0468569", Language: "brazillian-portuguese", Year: 2008, Releases: "[]"},
		{ID: "2001", SubsceneID: "2001", Slug: "breaking-bad-second-season",
			Title: "Breaking Bad Second Season", ImdbID: "tt0903747", Language: "english",
			Releases: `["Breaking.Bad.S02E05.720p.HDTV"]`},
	}
	if err := st.UpsertUploads(ctx, uploads); err != nil {
		t.Fatalf("seed uploads: %v", err)
	}

	files := []store.IngestFile{
		{ID: "f1", UploadID: "1001", Filename: "TDK.srt", Format: "srt",
			ContentHash: strings.Repeat("a", 64), ContentKey: "content/aa/aa/a.srt", Size: 100},
		{ID: "f2", UploadID: "1002", Filename: "TDK.HI.srt", Format: "srt",
			ContentHash: strings.Repeat("b", 64), ContentKey: "content/bb/bb/b.srt", Size: 100},
		{ID: "f3", UploadID: "1003", Filename: "TDK.pt.srt", Format: "srt",
			ContentHash: strings.Repeat("c", 64), ContentKey: "content/cc/cc/c.srt", Size: 100},
		{ID: "f4", UploadID: "2001", Filename: "BB.S02E05.srt", Format: "srt",
			ContentHash: strings.Repeat("d", 64), ContentKey: "content/dd/dd/d.srt", Size: 100},
	}
	if err := st.IngestFiles(ctx, files); err != nil {
		t.Fatalf("seed files: %v", err)
	}
	for _, f := range files {
		if err := stor.Put(ctx, f.ContentKey, strings.NewReader(subtitleBody), int64(len(subtitleBody))); err != nil {
			t.Fatalf("seed storage: %v", err)
		}
	}
	if err := st.Reindex(ctx); err != nil {
		t.Fatalf("reindex: %v", err)
	}

	srv := server.New(st, stor, "1.1.0-test")
	if err := srv.Warm(ctx); err != nil {
		t.Fatalf("warm: %v", err)
	}
	return api{handler: srv.Routes(), store: st, storage: stor}
}

func (a api) get(t *testing.T, url string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	a.handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))

	var body map[string]any
	if strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s returned invalid JSON: %v", url, err)
		}
	}
	return w, body
}

func items(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("response has no items array: %v", body)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		out = append(out, item.(map[string]any))
	}
	return out
}

// Bazarr's first call for a film.
func TestAPI_SearchByIMDBID(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st store.Store) {
		a := newAPI(t, st)

		w, body := a.get(t, "/api/v1/subtitles/search?imdb_id=tt0468569&language=english&per_page=100")
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body)
		}
		if body["total_items"].(float64) != 2 {
			t.Errorf("total_items = %v, want 2", body["total_items"])
		}

		first := items(t, body)[0]
		for field, want := range map[string]any{
			"title":    "The Dark Knight",
			"slug":     "the-dark-knight",
			"imdb_id":  "tt0468569",
			"language": "english",
			"format":   "srt",
		} {
			if first[field] != want {
				t.Errorf("%s = %v, want %v", field, first[field], want)
			}
		}
		if _, ok := first["releases"].([]any); !ok {
			t.Errorf("releases = %#v, want an array", first["releases"])
		}
		if !strings.HasSuffix(first["download_url"].(string), "/download") {
			t.Errorf("download_url = %v", first["download_url"])
		}
	})
}

// Bazarr's episode call: the series id plus a season and an episode.
func TestAPI_SearchByEpisode(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st store.Store) {
		a := newAPI(t, st)

		_, body := a.get(t, "/api/v1/subtitles/search?imdb_id=tt0903747&language=english&season=2&episode=5")
		if body["total_items"].(float64) != 1 {
			t.Errorf("total_items = %v, want 1", body["total_items"])
		}

		_, body = a.get(t, "/api/v1/subtitles/search?imdb_id=tt0903747&language=english&season=3&episode=1")
		if body["total_items"].(float64) != 0 {
			t.Errorf("total_items = %v, want 0 for an episode that is not there", body["total_items"])
		}
	})
}

// Bazarr's fallback when the IMDB search finds nothing.
func TestAPI_SearchByTitle(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st store.Store) {
		a := newAPI(t, st)

		_, body := a.get(t, "/api/v1/subtitles/search?query=The+Dark+Knight&language=english")
		if body["total_items"].(float64) != 2 {
			t.Errorf("total_items = %v, want 2", body["total_items"])
		}
	})
}

// The language that used to be unreachable because the spelling did not match.
func TestAPI_SearchInBrazilianPortuguese(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st store.Store) {
		a := newAPI(t, st)

		for _, spelling := range []string{"brazillian-portuguese", "Brazilian+Portuguese", "brazilian_portuguese"} {
			_, body := a.get(t, "/api/v1/subtitles/search?imdb_id=tt0468569&language="+spelling)
			if body["total_items"].(float64) != 1 {
				t.Errorf("language=%s returned %v results, want 1", spelling, body["total_items"])
			}
		}
	})
}

func TestAPI_HearingImpairedFilter(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st store.Store) {
		a := newAPI(t, st)

		cases := map[string]float64{
			"":          2,
			"&hi=true":  1,
			"&hi=false": 1,
		}
		for suffix, want := range cases {
			_, body := a.get(t, "/api/v1/subtitles/search?imdb_id=tt0468569&language=english"+suffix)
			if body["total_items"].(float64) != want {
				t.Errorf("hi%q returned %v, want %v", suffix, body["total_items"], want)
			}
		}
	})
}

// Every id a search hands out must be downloadable.
func TestAPI_DownloadEverySearchResult(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st store.Store) {
		a := newAPI(t, st)

		_, body := a.get(t, "/api/v1/subtitles/search?imdb_id=tt0468569&per_page=100")
		for _, item := range items(t, body) {
			url := item["download_url"].(string)
			path := url[strings.Index(url, "/api/v1"):]

			w, _ := a.get(t, path)
			if w.Code != http.StatusOK {
				t.Errorf("%s returned %d, want 200", path, w.Code)
				continue
			}
			if w.Body.String() != subtitleBody {
				t.Errorf("%s returned %q", path, w.Body.String())
			}
		}
	})
}

func TestAPI_DownloadCountsTowardsRanking(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st store.Store) {
		a := newAPI(t, st)

		if w, _ := a.get(t, "/api/v1/subtitles/f1/download"); w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}

		// The counter is incremented on a context that outlives the request, so
		// it lands just after the response.
		deadline := time.Now().Add(2 * time.Second)
		for {
			sub, err := st.GetSubtitle(context.Background(), "f1")
			if err != nil {
				t.Fatal(err)
			}
			if sub.Downloads == 1 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("downloads = %d, want 1", sub.Downloads)
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}

func TestAPI_DownloadUnknownID(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st store.Store) {
		a := newAPI(t, st)

		w, _ := a.get(t, "/api/v1/subtitles/does-not-exist/download")
		if w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", w.Code)
		}
	})
}

func TestAPI_Languages(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st store.Store) {
		a := newAPI(t, st)

		w, body := a.get(t, "/api/v1/languages")
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		if body["total_items"].(float64) != 2 {
			t.Errorf("total_items = %v, want 2", body["total_items"])
		}

		names := map[string]bool{}
		for _, item := range items(t, body) {
			names[item["name"].(string)] = true
		}
		if !names["english"] || !names["brazillian-portuguese"] {
			t.Errorf("languages = %v", names)
		}
	})
}

func TestAPI_Info(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st store.Store) {
		a := newAPI(t, st)

		w, body := a.get(t, "/api/v1/info")
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		if body["version"] != "1.1.0-test" {
			t.Errorf("version = %v", body["version"])
		}
		features := body["features"].(map[string]any)
		if features["search_by_imdb_id"] != true {
			t.Error("search_by_imdb_id should be true for a catalogue that has them")
		}
	})
}

func TestAPI_OpenAPIDocumentIsServed(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st store.Store) {
		a := newAPI(t, st)

		w, _ := a.get(t, "/api/v1/openapi.yaml")
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		for _, path := range []string{"/subtitles/search", "/subtitles/{id}/download", "/languages", "/info"} {
			if !strings.Contains(w.Body.String(), path) {
				t.Errorf("the served spec does not document %s", path)
			}
		}
	})
}

// A page beyond the end is an empty page, not an error: Bazarr walks pages.
func TestAPI_PageBeyondTheEnd(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st store.Store) {
		a := newAPI(t, st)

		w, body := a.get(t, "/api/v1/subtitles/search?imdb_id=tt0468569&page=99")
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		if len(items(t, body)) != 0 {
			t.Errorf("items = %v, want none", body["items"])
		}
		if body["total_items"].(float64) != 3 {
			t.Errorf("total_items = %v, want the exact match count even beyond the last page", body["total_items"])
		}
	})
}
