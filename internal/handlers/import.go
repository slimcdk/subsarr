package handlers

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
)

// importStatus holds running statistics for an ongoing import operation.
// It is package-level so the status endpoint can read it.
var importStatus = &importState{}

type importState struct {
	Running   atomic.Bool
	Total     atomic.Int64
	Imported  atomic.Int64
	Skipped   atomic.Int64
	Errors    atomic.Int64
	StartedAt atomic.Pointer[time.Time]
	LastError atomic.Pointer[string]
}

func (s *importState) snapshot() importStatusResponse {
	var started string
	if t := s.StartedAt.Load(); t != nil {
		started = t.Format(time.RFC3339)
	}
	var lastErr string
	if e := s.LastError.Load(); e != nil {
		lastErr = *e
	}
	return importStatusResponse{
		Running:   s.Running.Load(),
		Total:     s.Total.Load(),
		Imported:  s.Imported.Load(),
		Skipped:   s.Skipped.Load(),
		Errors:    s.Errors.Load(),
		StartedAt: started,
		LastError: lastErr,
	}
}

type importStatusResponse struct {
	Running   bool   `json:"running"`
	Total     int64  `json:"total"`
	Imported  int64  `json:"imported"`
	Skipped   int64  `json:"skipped"`
	Errors    int64  `json:"errors"`
	StartedAt string `json:"started_at,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

// NewImportStatusHandler returns current import progress.
func NewImportStatusHandler(_ *pocketbase.PocketBase) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		return e.JSON(http.StatusOK, importStatus.snapshot())
	}
}

// NewImportHandler handles multipart uploads of subscene dump ZIP files.
// It accepts one or more ZIP archives. Each archive may contain:
//   - Subtitle files (.srt, .ass, .sub, .ssa)
//   - An optional metadata sidecar (metadata.json or metadata.xml)
//
// The handler starts the import in a background goroutine and returns
// immediately so large imports don't time out the HTTP connection.
func NewImportHandler(app *pocketbase.PocketBase) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		if importStatus.Running.Load() {
			return e.JSON(http.StatusConflict, map[string]string{
				"error": "an import is already in progress",
			})
		}

		if err := e.Request.ParseMultipartForm(512 << 20); err != nil {
			return e.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}

		files := e.Request.MultipartForm.File["files"]
		if len(files) == 0 {
			return e.JSON(http.StatusBadRequest, map[string]string{
				"error": "no files uploaded – use multipart field name 'files'",
			})
		}

		// Read all uploaded archives into memory before handing off to the
		// background goroutine (multipart files are only valid during the request).
		archives := make([][]byte, 0, len(files))
		for _, fh := range files {
			f, err := fh.Open()
			if err != nil {
				return e.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
			}
			data, err := io.ReadAll(f)
			f.Close()
			if err != nil {
				return e.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
			}
			archives = append(archives, data)
		}

		go runImport(app, archives)

		return e.JSON(http.StatusAccepted, map[string]string{
			"message": fmt.Sprintf("import started for %d archive(s), poll /api/v1/import/status for progress", len(archives)),
		})
	}
}

// runImport processes the uploaded archives in a background goroutine.
func runImport(app *pocketbase.PocketBase, archives [][]byte) {
	now := time.Now()
	importStatus.Running.Store(true)
	importStatus.StartedAt.Store(&now)
	importStatus.Total.Store(0)
	importStatus.Imported.Store(0)
	importStatus.Skipped.Store(0)
	importStatus.Errors.Store(0)
	importStatus.LastError.Store(nil)

	defer importStatus.Running.Store(false)

	collection, err := app.FindCollectionByNameOrId("subtitles")
	if err != nil {
		msg := "collection 'subtitles' not found: " + err.Error()
		importStatus.LastError.Store(&msg)
		return
	}

	for _, data := range archives {
		processArchive(app, collection, data)
	}
}

type archiveEntry struct {
	name string
	data []byte
}

func processArchive(app *pocketbase.PocketBase, collection *core.Collection, data []byte) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		msg := "failed to open ZIP: " + err.Error()
		importStatus.LastError.Store(&msg)
		importStatus.Errors.Add(1)
		return
	}

	// Group files by directory (each sub-directory is typically one subtitle release)
	groups := map[string][]archiveEntry{}

	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			importStatus.Errors.Add(1)
			continue
		}
		raw, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			importStatus.Errors.Add(1)
			continue
		}
		dir := filepath.Dir(f.Name)
		groups[dir] = append(groups[dir], archiveEntry{name: f.Name, data: raw})
	}

	for _, entries := range groups {
		importStatus.Total.Add(1)
		if err := importGroup(app, collection, entries); err != nil {
			msg := err.Error()
			importStatus.LastError.Store(&msg)
			importStatus.Errors.Add(1)
		} else {
			importStatus.Imported.Add(1)
		}
	}
}

type subtitleMeta struct {
	Title       string `json:"title"        xml:"title"`
	Year        int    `json:"year"         xml:"year"`
	ImdbID      string `json:"imdb_id"      xml:"imdb_id"`
	Language    string `json:"language"     xml:"language"`
	ReleaseName string `json:"release_name" xml:"release_name"`
	Format      string `json:"format"       xml:"format"`
}

func importGroup(app *pocketbase.PocketBase, collection *core.Collection, entries []archiveEntry) error {
	var meta subtitleMeta
	var subtitleContent []byte
	var subtitleFilename string

	for _, e := range entries {
		base := filepath.Base(e.name)
		ext := strings.ToLower(filepath.Ext(base))

		switch {
		case base == "metadata.json":
			_ = json.Unmarshal(e.data, &meta)
		case base == "metadata.xml":
			_ = xml.Unmarshal(e.data, &meta)
		case ext == ".srt" || ext == ".ass" || ext == ".ssa" || ext == ".sub":
			subtitleContent = e.data
			subtitleFilename = base
			if meta.Format == "" {
				meta.Format = strings.TrimPrefix(ext, ".")
			}
		}
	}

	if len(subtitleContent) == 0 {
		importStatus.Skipped.Add(1)
		return nil // no subtitle file in this group
	}

	// Fall back to deriving metadata from the filename when no sidecar exists
	if meta.ReleaseName == "" {
		meta.ReleaseName = strings.TrimSuffix(subtitleFilename, filepath.Ext(subtitleFilename))
	}
	if meta.Language == "" {
		meta.Language = "unknown"
	}
	if meta.Title == "" {
		meta.Title = meta.ReleaseName
	}

	record := core.NewRecord(collection)
	record.Set("title", meta.Title)
	record.Set("year", meta.Year)
	record.Set("imdb_id", meta.ImdbID)
	record.Set("language", meta.Language)
	record.Set("release_name", meta.ReleaseName)
	record.Set("filename", subtitleFilename)
	record.Set("format", meta.Format)
	record.Set("content", string(subtitleContent))
	record.Set("downloads", 0)

	return app.Save(record)
}
