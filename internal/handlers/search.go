package handlers

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
)

type subtitleResult struct {
	ID          string `json:"id"`
	SubsceneID  string `json:"subscene_id"`
	Title       string `json:"title"`
	Slug        string `json:"slug"`
	ImdbID      string `json:"imdb_id"`
	Language    string `json:"language"`
	HI          bool   `json:"hi"`
	Author      string `json:"author"`
	Releases    any    `json:"releases"`
	Filename    string `json:"filename"`
	Format      string `json:"format"`
	UploadedAt  string `json:"uploaded_at,omitempty"`
	Downloads   int    `json:"downloads"`
	DownloadURL string `json:"download_url"`
}

// NewSearchHandler searches the subtitles collection.
//
// Query params:
//   - query     – free-text search against title / filename
//   - language  – language name as stored in Subscene (e.g. "English", "Greek")
//   - imdb_id   – IMDB tt-ID (e.g. "tt1234567")
//   - slug      – Subscene URL slug (e.g. "the-dark-knight")
//   - hi        – "true" to filter hearing-impaired only
//   - year      – release year (if populated)
//   - season    – season number; filters releases/filename for S<NN> pattern
//   - episode   – episode number; combined with season gives S<NN>E<NN> pattern
//   - page      – page number, 1-based (default 1)
//   - per_page  – results per page, max 200 (default 50)
func NewSearchHandler(app *pocketbase.PocketBase) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		q := e.Request.URL.Query()

		page := max(1, intParam(q.Get("page"), 1))
		perPage := clamp(intParam(q.Get("per_page"), 50), 1, 200)
		offset := (page - 1) * perPage

		filterExpr := "1=1"
		params := dbx.Params{}

		if v := q.Get("imdb_id"); v != "" {
			filterExpr += " && imdb_id = {:imdb_id}"
			params["imdb_id"] = v
		}
		if v := q.Get("language"); v != "" {
			filterExpr += " && language = {:language}"
			params["language"] = v
		}
		if v := q.Get("slug"); v != "" {
			filterExpr += " && slug = {:slug}"
			params["slug"] = v
		}
		if q.Get("hi") == "true" {
			filterExpr += " && hi = true"
		}
		if v := q.Get("year"); v != "" {
			filterExpr += " && year = {:year}"
			params["year"] = v
		}
		if v := q.Get("query"); v != "" {
			filterExpr += " && (title ~ {:query} || filename ~ {:query})"
			params["query"] = v
		}

		// Season / episode filtering — matches patterns like S01 or S01E05 inside
		// the releases JSON array or filename (e.g. "Show.S01E05.1080p...").
		if s := q.Get("season"); s != "" {
			if sn, err := strconv.Atoi(s); err == nil {
				var pattern string
				if ep := intParam(q.Get("episode"), 0); ep > 0 {
					pattern = fmt.Sprintf("S%02dE%02d", sn, ep)
				} else {
					pattern = fmt.Sprintf("S%02d", sn)
				}
				filterExpr += " && (releases ~ {:season_ep} || filename ~ {:season_ep})"
				params["season_ep"] = pattern
			}
		}

		records, err := app.FindRecordsByFilter("subtitles", filterExpr, "-downloads", perPage, offset, params)
		if err != nil {
			return e.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}

		baseURL := requestBaseURL(e.Request.URL, e.Request.Host, e.Request.TLS != nil)
		results := make([]subtitleResult, 0, len(records))
		for _, r := range records {
			results = append(results, subtitleResult{
				ID:          r.Id,
				SubsceneID:  r.GetString("subscene_id"),
				Title:       r.GetString("title"),
				Slug:        r.GetString("slug"),
				ImdbID:      r.GetString("imdb_id"),
				Language:    r.GetString("language"),
				HI:          r.GetBool("hi"),
				Author:      r.GetString("author"),
				Releases:    r.Get("releases"),
				Filename:    r.GetString("filename"),
				Format:      r.GetString("format"),
				UploadedAt:  r.GetString("uploaded_at"),
				Downloads:   r.GetInt("downloads"),
				DownloadURL: fmt.Sprintf("%s/api/v1/subtitles/%s/download", baseURL, r.Id),
			})
		}

		return e.JSON(http.StatusOK, map[string]any{
			"items":       results,
			"total_items": len(results),
			"page":        page,
			"per_page":    perPage,
		})
	}
}

// requestBaseURL derives the scheme+host from an incoming request.
func requestBaseURL(u *url.URL, host string, tls bool) string {
	scheme := "http"
	if tls || u.Scheme == "https" {
		scheme = "https"
	}
	if h := u.Host; h != "" {
		host = h
	}
	return scheme + "://" + host
}

func intParam(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
