package handlers

import (
	"fmt"
	"net/http"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
)

// NewDownloadHandler increments the download counter then redirects to
// PocketBase's built-in file serving endpoint for the subtitle content file.
func NewDownloadHandler(app *pocketbase.PocketBase) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		id := e.Request.PathValue("id")
		if id == "" {
			return e.JSON(http.StatusBadRequest, map[string]string{"error": "missing id"})
		}

		record, err := app.FindRecordById("subtitles", id)
		if err != nil {
			return e.JSON(http.StatusNotFound, map[string]string{"error": "subtitle not found"})
		}

		// FileField stores the generated filename (e.g. "Show.S01E01.srt_abc123.srt").
		storedFilename := record.GetString("content")
		if storedFilename == "" {
			return e.JSON(http.StatusNotFound, map[string]string{"error": "subtitle content not available"})
		}

		// Increment download counter (best-effort).
		record.Set("downloads", record.GetInt("downloads")+1)
		_ = app.Save(record)

		// Redirect to PocketBase's file serving endpoint.
		// ?download=1 forces Content-Disposition: attachment instead of inline.
		fileURL := fmt.Sprintf("/api/files/subtitles/%s/%s?download=1", id, storedFilename)
		http.Redirect(e.Response, e.Request, fileURL, http.StatusFound)
		return nil
	}
}
