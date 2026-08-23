package server

import "net/http"

type providerInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Version     string   `json:"version"`
	Features    features `json:"features"`
}

type features struct {
	SearchByIMDB    bool `json:"search_by_imdb_id"`
	SearchByTitle   bool `json:"search_by_title"`
	SearchBySlug    bool `json:"search_by_slug"`
	SearchBySeason  bool `json:"search_by_season_episode"`
	HearingImpaired bool `json:"hearing_impaired_filter"`
	LanguageFilter  bool `json:"language_filter"`
}

// handleInfo reports what this installation can actually do. search_by_imdb_id
// is read from the data rather than declared: an installation imported before the
// catalogue existed has no IMDB ids, and a client that trusted a hardcoded true
// would search for them anyway and find nothing.
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	cat, err := s.loadCatalogue(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, providerInfo{
		Name:        "subsarr",
		Description: "Subscene subtitle database provider",
		Version:     s.version,
		Features: features{
			SearchByIMDB:    cat.hasIMDB,
			SearchByTitle:   true,
			SearchBySlug:    true,
			SearchBySeason:  true,
			HearingImpaired: true,
			LanguageFilter:  len(cat.languages) > 0,
		},
	})
}
