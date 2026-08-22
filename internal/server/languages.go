package server

import "net/http"

type languageEntry struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// handleLanguages lists the languages that actually have subtitle files, so that
// what the API advertises is what it can deliver.
func (s *Server) handleLanguages(w http.ResponseWriter, r *http.Request) {
	cat, err := s.loadCatalogue(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":       cat.languages,
		"total_items": len(cat.languages),
	})
}
