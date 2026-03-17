package server

import (
	"encoding/json"
	"net/http"
)

type languageEntry struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func (s *Server) handleLanguages(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListLanguages(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	langs := make([]languageEntry, 0, len(rows))
	for _, r := range rows {
		langs = append(langs, languageEntry{Name: r.Language, Count: r.Count})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"items":       langs,
		"total_items": len(langs),
	})
}
