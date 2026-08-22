package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/slimcdk/subsarr/internal/lang"
	"github.com/slimcdk/subsarr/internal/store"
)

type subtitleResult struct {
	ID          string   `json:"id"`
	SubsceneID  string   `json:"subscene_id"`
	Title       string   `json:"title"`
	Slug        string   `json:"slug"`
	ImdbID      string   `json:"imdb_id"`
	Language    string   `json:"language"`
	HI          bool     `json:"hi"`
	Author      string   `json:"author"`
	Releases    []string `json:"releases"`
	Comment     string   `json:"comment,omitempty"`
	Year        int      `json:"year,omitempty"`
	Filename    string   `json:"filename"`
	Format      string   `json:"format"`
	UploadedAt  string   `json:"uploaded_at,omitempty"`
	Downloads   int      `json:"downloads"`
	DownloadURL string   `json:"download_url"`
}

// handleSearch answers Bazarr's four query shapes: by IMDB id, by IMDB id with
// season and episode, by title, and by title with season and episode.
//
// Validation is deliberately lenient. Bazarr is a released client that cannot be
// fixed from here, so a 400 is reserved for input no reading of which is valid —
// a page that is not a number, a hi that is neither true nor false. Anything a
// client might plausibly send is interpreted, not rejected.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	page, err := intParam(q, "page", 1, 1)
	if err != nil {
		badRequest(w, err)
		return
	}
	perPage, err := intParam(q, "per_page", 50, 1)
	if err != nil {
		badRequest(w, err)
		return
	}
	perPage = min(perPage, maxPerPage)

	season, err := intParam(q, "season", 0, 0)
	if err != nil {
		badRequest(w, err)
		return
	}
	episode, err := intParam(q, "episode", 0, 0)
	if err != nil {
		badRequest(w, err)
		return
	}
	year, err := intParam(q, "year", 0, 0)
	if err != nil {
		badRequest(w, err)
		return
	}
	hi, err := boolParam(q, "hi")
	if err != nil {
		badRequest(w, err)
		return
	}

	params := store.SearchParams{
		ImdbID: q.Get("imdb_id"),
		// The stored language is canonical, so the requested one has to be too:
		// this is what makes a spelling Bazarr uses reach the rows it means.
		Language: lang.Canonical(q.Get("language")),
		Slug:     q.Get("slug"),
		Query:    q.Get("query"),
		HI:       hi,
		Year:     year,
		SeasonEp: seasonEpisode(season, episode),
		Limit:    perPage,
		Offset:   (page - 1) * perPage,
	}

	records, total, err := s.searchStore(r, params)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	baseURL := requestBaseURL(r.URL, r.Host, r.TLS != nil)
	results := make([]subtitleResult, 0, len(records))
	for _, rec := range records {
		results = append(results, subtitleResult{
			ID:          rec.ID,
			SubsceneID:  rec.SubsceneID,
			Title:       rec.Title,
			Slug:        rec.Slug,
			ImdbID:      rec.ImdbID,
			Language:    rec.Language,
			HI:          rec.HI,
			Author:      rec.Author,
			Releases:    decodeReleases(rec.Releases),
			Comment:     rec.Comment,
			Year:        rec.Year,
			Filename:    rec.Filename,
			Format:      rec.Format,
			UploadedAt:  rec.UploadedAt,
			Downloads:   rec.Downloads,
			DownloadURL: fmt.Sprintf("%s/api/v1/subtitles/%s/download", baseURL, rec.ID),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":       results,
		"total_items": total,
		"page":        page,
		"per_page":    perPage,
	})
}

// searchStore short-circuits an IMDB search on a catalogue that has no IMDB ids
// at all — an installation imported before the catalogue existed — so the miss
// costs nothing instead of a scan that cannot succeed.
func (s *Server) searchStore(r *http.Request, params store.SearchParams) ([]store.Subtitle, int, error) {
	if params.ImdbID != "" {
		cat, err := s.loadCatalogue(r.Context())
		if err != nil {
			return nil, 0, err
		}
		if !cat.hasIMDB {
			return nil, 0, nil
		}
	}
	return s.store.SearchSubtitles(r.Context(), params)
}

// seasonEpisode renders the pattern that is matched against release names and
// file names. An episode without a season is ignored: on its own it matches
// numbers that mean something else.
func seasonEpisode(season, episode int) string {
	switch {
	case season <= 0:
		return ""
	case episode > 0:
		return fmt.Sprintf("S%02dE%02d", season, episode)
	default:
		return fmt.Sprintf("S%02d", season)
	}
}

// decodeReleases turns the stored JSON array into the array the API promises.
// `releases` is documented as always being an array, so a row holding anything
// else answers with an empty one rather than changing the response's shape.
func decodeReleases(raw string) []string {
	releases := []string{}
	if raw == "" {
		return releases
	}
	if err := json.Unmarshal([]byte(raw), &releases); err != nil || releases == nil {
		return []string{}
	}
	return releases
}
