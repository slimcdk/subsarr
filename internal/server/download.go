package server

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
)

// handleDownload streams one subtitle file.
//
// Every id a search returns is downloadable: a row only exists when its content
// was stored, so a 404 here means the id is not ours, not that the service
// advertised something it does not have.
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		badRequest(w, errors.New("missing id"))
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

	rc, err := s.storage.Get(r.Context(), record.ContentKey)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "subtitle file not found"})
		return
	}
	defer rc.Close()

	// The counter is part of the ranking, so it must not be lost when the client
	// disconnects the moment it has the bytes. WithoutCancel keeps the request's
	// values while dropping its deadline.
	go s.store.IncrementDownloads(context.WithoutCancel(r.Context()), id)

	filename := record.Filename
	if filename == "" {
		filename = filepath.Base(record.ContentKey)
	}

	w.Header().Set("Content-Disposition", `attachment; filename="`+sanitiseFilename(filename)+`"`)
	w.Header().Set("Content-Type", "application/octet-stream")
	io.Copy(w, rc)
}

// sanitiseFilename keeps a subtitle's own name from breaking the header it is
// quoted in. Subscene file names contain anything a user typed.
func sanitiseFilename(name string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '"', '\\', '\r', '\n':
			return -1
		}
		if r < 0x20 {
			return -1
		}
		return r
	}, name)
}
