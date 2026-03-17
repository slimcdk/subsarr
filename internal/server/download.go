package server

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"path/filepath"
)

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}

	record, err := s.store.GetSubtitle(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "subtitle not found"})
		} else {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return
	}

	if record.ContentKey == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "subtitle content not available"})
		return
	}

	rc, err := s.storage.Get(r.Context(), record.ContentKey)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "subtitle file not found"})
		return
	}
	defer rc.Close()

	// Increment download counter (best-effort).
	go s.store.IncrementDownloads(r.Context(), id)

	filename := record.Filename
	if filename == "" {
		filename = filepath.Base(record.ContentKey)
	}

	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	w.Header().Set("Content-Type", "application/octet-stream")
	io.Copy(w, rc)
}
