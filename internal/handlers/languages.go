package handlers

import (
	"net/http"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
)

type languageEntry struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// NewLanguagesHandler returns the distinct languages present in the database
// together with how many subtitles exist for each. Useful for a Bazarr provider
// to discover which languages are available before issuing a search.
func NewLanguagesHandler(app *pocketbase.PocketBase) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		type row struct {
			Language string `db:"language"`
			Count    int    `db:"count"`
		}

		var rows []row
		err := app.ConcurrentDB().
			NewQuery("SELECT language, COUNT(*) AS count FROM subtitles WHERE language != '' GROUP BY language ORDER BY count DESC").
			All(&rows)
		if err != nil {
			return e.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}

		langs := make([]languageEntry, 0, len(rows))
		for _, r := range rows {
			langs = append(langs, languageEntry{Name: r.Language, Count: r.Count})
		}

		return e.JSON(http.StatusOK, map[string]any{
			"items":       langs,
			"total_items": len(langs),
		})
	}
}
