package handlers

import (
	"net/http"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
)

type providerInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Version     string   `json:"version"`
	Features    features `json:"features"`
}

type features struct {
	SearchByIMDB     bool `json:"search_by_imdb_id"`
	SearchByTitle    bool `json:"search_by_title"`
	SearchBySlug     bool `json:"search_by_slug"`
	SearchBySeason   bool `json:"search_by_season_episode"`
	HearingImpaired  bool `json:"hearing_impaired_filter"`
	LanguageFilter   bool `json:"language_filter"`
}

// NewInfoHandler returns static metadata about this provider instance.
// Bazarr (or any client) can call this to discover capabilities before querying.
func NewInfoHandler(_ *pocketbase.PocketBase) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		return e.JSON(http.StatusOK, providerInfo{
			Name:        "subsarr",
			Description: "Subscene subtitle database provider",
			Version:     "1.0.0",
			Features: features{
				SearchByIMDB:    true,
				SearchByTitle:   true,
				SearchBySlug:    true,
				SearchBySeason:  true,
				HearingImpaired: true,
				LanguageFilter:  true,
			},
		})
	}
}
