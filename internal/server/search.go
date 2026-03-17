package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/slimcdk/subsarr/internal/store"
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

// handleSearch queries the subtitles store.
//
// Query params: query, language, imdb_id, slug, hi, year, season, episode, page, per_page.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	page := max(1, intParam(q.Get("page"), 1))
	perPage := clamp(intParam(q.Get("per_page"), 50), 1, 200)
	offset := (page - 1) * perPage

	params := store.SearchParams{
		ImdbID:   q.Get("imdb_id"),
		Language: q.Get("language"),
		Slug:     q.Get("slug"),
		Query:    q.Get("query"),
		Year:     intParam(q.Get("year"), 0),
		Limit:    perPage,
		Offset:   offset,
	}

	if q.Get("hi") == "true" {
		hi := true
		params.HI = &hi
	}

	if sn := q.Get("season"); sn != "" {
		if n, err := strconv.Atoi(sn); err == nil {
			if ep := intParam(q.Get("episode"), 0); ep > 0 {
				params.SeasonEp = fmt.Sprintf("S%02dE%02d", n, ep)
			} else {
				params.SeasonEp = fmt.Sprintf("S%02d", n)
			}
		}
	}

	records, err := s.store.SearchSubtitles(r.Context(), params)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	baseURL := requestBaseURL(r.URL, r.Host, r.TLS != nil)
	results := make([]subtitleResult, 0, len(records))
	for _, rec := range records {
		var releases any
		if err := json.Unmarshal([]byte(rec.Releases), &releases); err != nil {
			releases = rec.Releases
		}
		results = append(results, subtitleResult{
			ID:          rec.ID,
			SubsceneID:  rec.SubsceneID,
			Title:       rec.Title,
			Slug:        rec.Slug,
			ImdbID:      rec.ImdbID,
			Language:    rec.Language,
			HI:          rec.HI,
			Author:      rec.Author,
			Releases:    releases,
			Filename:    rec.Filename,
			Format:      rec.Format,
			UploadedAt:  rec.UploadedAt,
			Downloads:   rec.Downloads,
			DownloadURL: fmt.Sprintf("%s/api/v1/subtitles/%s/download", baseURL, rec.ID),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"items":       results,
		"total_items": len(results),
		"page":        page,
		"per_page":    perPage,
	})
}
