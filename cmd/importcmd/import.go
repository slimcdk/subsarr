// Package importcmd provides a CLI command to import Subscene subtitle dumps
// into the PocketBase database.
//
// # One-shot streaming from archive (no extraction to disk)
//
//	./subsarr import-dump --archive "/path/to/Subscene V2.7z.001"
//
// The command opens the split 7z archive in streaming mode using a pure-Go
// reader (github.com/bodgit/sevenzip), auto-detects the dump format (V1 or V2),
// and imports records directly — no temporary files, no extra disk space needed.
//
// # Manual V1 — "Subscene Final" (extracted metadata.json + subtitles/ dir)
//
//	./subsarr import-dump --metadata /path/to/metadata.json \
//	                      --subtitles /path/to/subtitles/
//
// # Manual V2 — "Subscene V2" (extracted Subscene Files DB/ directory tree)
//
//	./subsarr import-dump --files-db "/path/to/Subscene Files DB/"
package importcmd

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bodgit/sevenzip"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/filesystem"
	"github.com/spf13/cobra"
)

var imdbRe = regexp.MustCompile(`/title/(tt\d+)`)
var nonAlnumRe = regexp.MustCompile(`[^a-z0-9]+`)

// metaEntry mirrors one JSON object in a V1 metadata.json.
type metaEntry struct {
	SubsceneID string   `json:"subscene_id"`
	Title      string   `json:"title"`
	Language   string   `json:"language"`
	Author     string   `json:"author"`
	Releases   []string `json:"releases"`
	Comment    string   `json:"comment"`
	Download   string   `json:"download"`
	Original   string   `json:"original"`
	IMDB       string   `json:"imdb"`
	Date       string   `json:"date"`
}

// subFile holds a subtitle file's name and raw content.
type subFile struct {
	filename string
	format   string
	content  []byte
}

// suppressSQLLogging nulls out the dbx query/exec log funcs on every DB
// connection so the import output is not flooded with SQL statements.
func suppressSQLLogging(app *pocketbase.PocketBase) {
	for _, b := range []dbx.Builder{
		app.ConcurrentDB(), app.NonconcurrentDB(),
		app.AuxConcurrentDB(), app.AuxNonconcurrentDB(),
	} {
		if db, ok := b.(*dbx.DB); ok {
			db.QueryLogFunc = nil
			db.ExecLogFunc = nil
		}
	}
}

// MustRegister adds the import-dump cobra command to the PocketBase root command.
func MustRegister(app *pocketbase.PocketBase) {
	cmd := &cobra.Command{
		Use:   "import-dump",
		Short: "Import a Subscene dump into the database",
		Long: `Import subtitle records from a Subscene dump.

Streaming from archive (no disk space needed, no extraction step):
  subsarr import-dump --archive "/path/to/Subscene V2.7z.001"
  subsarr import-dump --archive "/path/to/Subscene Final.7z.001"

Manual V1 (from already-extracted metadata.json + subtitles/ directory):
  subsarr import-dump --metadata /path/to/metadata.json \
                      --subtitles /path/to/subtitles/

Manual V2 (from already-extracted Subscene Files DB/ directory):
  subsarr import-dump --files-db "/path/to/Subscene Files DB/"`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := app.Bootstrap(); err != nil {
				return fmt.Errorf("bootstrap: %w", err)
			}

			// Suppress SQL logging — subtitle content is tens of KB per record
			// and floods the output. PocketBase auto-enables SQL logging when
			// run via "go run" (dev mode).
			suppressSQLLogging(app)

			archive, _ := cmd.Flags().GetString("archive")
			metaPath, _ := cmd.Flags().GetString("metadata")
			subsDir, _ := cmd.Flags().GetString("subtitles")
			filesDB, _ := cmd.Flags().GetString("files-db")
			batchSize, _ := cmd.Flags().GetInt("batch")
			limit, _ := cmd.Flags().GetInt("limit")

			switch {
			case archive != "":
				return runFromArchive(app, archive, batchSize, limit)
			case filesDB != "":
				return runV2(app, filesDB, batchSize, limit)
			case metaPath != "":
				return runV1(app, metaPath, subsDir, batchSize, limit)
			default:
				return fmt.Errorf("provide --archive, --metadata (V1), or --files-db (V2)")
			}
		},
	}

	cmd.Flags().String("archive", "", "Path to first volume of split 7z archive — streams directly, no extraction needed")
	cmd.Flags().String("metadata", "", "V1: path to metadata.json")
	cmd.Flags().String("subtitles", "", "V1: path to subtitles/ directory (optional; omit for metadata-only)")
	cmd.Flags().String("files-db", "", "V2: path to 'Subscene Files DB/' directory")
	cmd.Flags().Int("batch", 500, "Records per DB transaction")
	cmd.Flags().Int("limit", 0, "Stop after this many source entries (0 = all)")

	app.RootCmd.AddCommand(cmd)
}

// ─── streaming from archive ───────────────────────────────────────────────────

// runFromArchive opens the split 7z archive with a pure-Go reader, detects
// the dump format and imports without extracting to disk.
func runFromArchive(app *pocketbase.PocketBase, archivePath string, batchSize, limit int) error {
	col, err := app.FindCollectionByNameOrId("subtitles")
	if err != nil {
		return fmt.Errorf("collection 'subtitles' not found – run migrations first: %w", err)
	}

	log.Printf("[import] opening archive %s …", archivePath)
	r, err := sevenzip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer r.Close()

	log.Printf("[import] archive has %d entries across volumes: %v", len(r.File), r.Volumes())

	// Detect format by scanning the file list (no decompression needed).
	format := detectArchiveFormat(r)
	log.Printf("[import] detected format: %s", format)

	switch format {
	case "v1":
		return streamV1(app, r, col, batchSize, limit)
	case "v2":
		return streamV2(app, r, col, batchSize, limit)
	default:
		return fmt.Errorf("unrecognised archive layout — expected metadata.json (V1) or 'Subscene Files DB/' (V2)")
	}
}

// detectArchiveFormat scans the archive's file list (headers only, no I/O) to
// determine whether it is a V1 or V2 dump.
func detectArchiveFormat(r *sevenzip.ReadCloser) string {
	for _, f := range r.File {
		base := filepath.Base(f.Name)
		if base == "metadata.json" {
			return "v1"
		}
		if strings.Contains(f.Name, "Subscene Files DB/") {
			return "v2"
		}
	}
	return "unknown"
}

// streamV2 iterates the archive file list and, for every ZIP that lives under
// "Subscene Files DB/{slug}/", reads it in memory and inserts subtitle records.
func streamV2(app *pocketbase.PocketBase, r *sevenzip.ReadCloser, col *core.Collection, batchSize, limit int) error {
	imp := newImporter(app, col, batchSize)
	defer imp.flush()

	total := len(r.File)

	for i, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if strings.ToLower(filepath.Ext(f.Name)) != ".zip" {
			continue
		}
		if !strings.Contains(f.Name, "Subscene Files DB/") {
			continue
		}
		if limit > 0 && imp.total >= limit {
			break
		}

		imp.total++

		// path: …/Subscene Files DB/{slug}/{slug}[_HI]_{lang}-{id}.zip
		slug := filepath.Base(filepath.Dir(f.Name))
		filename := filepath.Base(f.Name)
		subsceneID, language, hi := parseV2Filename(slug, filename)
		if subsceneID == "" {
			if imp.errCount < 5 {
				log.Printf("[import] skipping unparseable filename: %s", f.Name)
			}
			imp.errCount++
			continue
		}

		data, err := readArchiveFile(f)
		if err != nil {
			if imp.errCount < 5 {
				log.Printf("[import] skipping unreadable archive entry %s: %v", f.Name, err)
			}
			imp.errCount++
			continue
		}

		files := extractSubtitlesFromZIPBytes(data)
		if len(files) == 0 {
			// Empty or unreadable ZIP — store a metadata-only record so the
			// entry is still searchable even without subtitle content.
			files = []subFile{{filename: filename, format: "zip"}}
		}

		title := slugToTitle(slug)
		for _, sf := range files {
			rec := core.NewRecord(col)
			rec.Set("subscene_id", subsceneID)
			rec.Set("title", title)
			rec.Set("slug", slug)
			rec.Set("language", language)
			rec.Set("hi", hi)
			rec.Set("filename", sf.filename)
			rec.Set("format", sf.format)
			setContent(rec, sf)
			rec.Set("downloads", 0)
			imp.add(rec)
		}

		if i%5_000 == 0 {
			log.Printf("[import] scanned %d/%d archive entries", i, total)
		}
	}

	imp.logFinal("V2 stream")
	return nil
}

// streamV1 does two passes over the archive:
//  1. Read metadata.json and build an in-memory lookup map.
//  2. Iterate ZIPs in subtitles/, enriching each record with the map.
func streamV1(app *pocketbase.PocketBase, r *sevenzip.ReadCloser, col *core.Collection, batchSize, limit int) error {
	// Pass 1 — build metadata map.
	log.Printf("[import] V1 pass 1/2: loading metadata.json …")
	meta, err := loadV1Metadata(r)
	if err != nil {
		return err
	}
	log.Printf("[import] loaded %d metadata entries", len(meta))

	// Pass 2 — import ZIPs.
	imp := newImporter(app, col, batchSize)
	defer imp.flush()

	for _, f := range r.File {
		if f.FileInfo().IsDir() || !strings.HasPrefix(f.Name, "subtitles/") {
			continue
		}
		if strings.ToLower(filepath.Ext(f.Name)) != ".zip" {
			continue
		}
		if limit > 0 && imp.total >= limit {
			break
		}
		imp.total++

		zipName := filepath.Base(f.Name) // e.g. "loki-second-season_greek-3193694.zip"
		entry, ok := meta[zipName]
		if !ok {
			// fallback: infer from filename alone
			entry.Language = "unknown"
		}

		hi := strings.Contains(zipName, "_HI_")
		slug := extractSlug(entry.Original)
		imdbID := extractIMDB(entry.IMDB)
		title := html.UnescapeString(entry.Title)
		uploadedAt := parseDate(entry.Date)

		data, err := readArchiveFile(f)
		if err != nil || len(data) == 0 {
			imp.errCount++
			continue
		}

		files := extractSubtitlesFromZIPBytes(data)
		if len(files) == 0 {
			ext := strings.ToLower(filepath.Ext(zipName))
			files = []subFile{{filename: zipName, format: strings.TrimPrefix(ext, ".")}}
		}

		for _, sf := range files {
			rec := core.NewRecord(col)
			rec.Set("subscene_id", entry.SubsceneID)
			rec.Set("title", title)
			rec.Set("slug", slug)
			rec.Set("imdb_id", imdbID)
			rec.Set("language", entry.Language)
			rec.Set("hi", hi)
			rec.Set("author", entry.Author)
			rec.Set("releases", entry.Releases)
			rec.Set("comment", entry.Comment)
			rec.Set("filename", sf.filename)
			rec.Set("format", sf.format)
			setContent(rec, sf)
			rec.Set("downloads", 0)
			if !uploadedAt.IsZero() {
				rec.Set("uploaded_at", uploadedAt)
			}
			imp.add(rec)
		}
	}

	imp.logFinal("V1 stream")
	return nil
}

// loadV1Metadata reads metadata.json from the archive and returns a map keyed
// by the download filename (e.g. "loki-second-season_greek-3193694.zip").
func loadV1Metadata(r *sevenzip.ReadCloser) (map[string]metaEntry, error) {
	for _, f := range r.File {
		if filepath.Base(f.Name) != "metadata.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open metadata.json in archive: %w", err)
		}
		defer rc.Close()

		m := make(map[string]metaEntry, 3_000_000)
		dec := json.NewDecoder(rc)
		if _, err := dec.Token(); err != nil { // consume '['
			return nil, fmt.Errorf("metadata.json is not a JSON array: %w", err)
		}
		for dec.More() {
			var e metaEntry
			if err := dec.Decode(&e); err != nil {
				continue
			}
			m[e.Download] = e
		}
		return m, nil
	}
	return nil, fmt.Errorf("metadata.json not found in archive")
}

// readArchiveFile reads the full content of a sevenzip file entry into memory.
func readArchiveFile(f *sevenzip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// ─── manual V1 (extracted) ────────────────────────────────────────────────────

func runV1(app *pocketbase.PocketBase, metaPath, subsDir string, batchSize, limit int) error {
	col, err := app.FindCollectionByNameOrId("subtitles")
	if err != nil {
		return fmt.Errorf("collection 'subtitles' not found – run migrations first: %w", err)
	}

	f, err := os.Open(metaPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", metaPath, err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	if _, err := dec.Token(); err != nil {
		return fmt.Errorf("metadata.json does not start with '[': %w", err)
	}

	imp := newImporter(app, col, batchSize)
	defer imp.flush()

	for dec.More() {
		if limit > 0 && imp.total >= limit {
			break
		}
		var entry metaEntry
		if err := dec.Decode(&entry); err != nil {
			imp.errCount++
			continue
		}
		imp.total++

		hi := strings.Contains(entry.Download, "_HI_")
		slug := extractSlug(entry.Original)
		imdbID := extractIMDB(entry.IMDB)
		title := html.UnescapeString(entry.Title)
		uploadedAt := parseDate(entry.Date)
		ext := strings.ToLower(filepath.Ext(entry.Download))

		var files []subFile
		if subsDir != "" && ext == ".zip" {
			files = extractSubtitlesFromZIPPath(filepath.Join(subsDir, entry.Download))
		}
		if len(files) == 0 {
			files = []subFile{{filename: entry.Download, format: strings.TrimPrefix(ext, ".")}}
		}

		for _, sf := range files {
			rec := core.NewRecord(col)
			rec.Set("subscene_id", entry.SubsceneID)
			rec.Set("title", title)
			rec.Set("slug", slug)
			rec.Set("imdb_id", imdbID)
			rec.Set("language", entry.Language)
			rec.Set("hi", hi)
			rec.Set("author", entry.Author)
			rec.Set("releases", entry.Releases)
			rec.Set("comment", entry.Comment)
			rec.Set("filename", sf.filename)
			rec.Set("format", sf.format)
			setContent(rec, sf)
			rec.Set("downloads", 0)
			if !uploadedAt.IsZero() {
				rec.Set("uploaded_at", uploadedAt)
			}
			imp.add(rec)
		}
	}

	imp.logFinal("V1")
	return nil
}

// ─── manual V2 (extracted) ────────────────────────────────────────────────────

func runV2(app *pocketbase.PocketBase, filesDBDir string, batchSize, limit int) error {
	col, err := app.FindCollectionByNameOrId("subtitles")
	if err != nil {
		return fmt.Errorf("collection 'subtitles' not found – run migrations first: %w", err)
	}

	imp := newImporter(app, col, batchSize)
	defer imp.flush()

	err = filepath.WalkDir(filesDBDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		if strings.ToLower(filepath.Ext(path)) != ".zip" {
			return nil
		}
		if limit > 0 && imp.total >= limit {
			return fs.SkipAll
		}
		imp.total++

		slug := filepath.Base(filepath.Dir(path))
		subsceneID, language, hi := parseV2Filename(slug, filepath.Base(path))
		if subsceneID == "" {
			imp.errCount++
			return nil
		}

		files := extractSubtitlesFromZIPPath(path)
		if len(files) == 0 {
			files = []subFile{{filename: filepath.Base(path), format: "zip"}}
		}

		title := slugToTitle(slug)
		for _, sf := range files {
			rec := core.NewRecord(col)
			rec.Set("subscene_id", subsceneID)
			rec.Set("title", title)
			rec.Set("slug", slug)
			rec.Set("language", language)
			rec.Set("hi", hi)
			rec.Set("filename", sf.filename)
			rec.Set("format", sf.format)
			setContent(rec, sf)
			rec.Set("downloads", 0)
			imp.add(rec)
		}
		return nil
	})

	imp.logFinal("V2")
	return err
}

// ─── shared importer ─────────────────────────────────────────────────────────

type importer struct {
	app       *pocketbase.PocketBase
	col       *core.Collection
	batchSize int
	batch     []*core.Record
	total     int
	imported  int
	skipped   int
	errCount  int
	start     time.Time
}

func newImporter(app *pocketbase.PocketBase, col *core.Collection, batchSize int) *importer {
	return &importer{app: app, col: col, batchSize: batchSize, start: time.Now()}
}

func (im *importer) add(rec *core.Record) {
	im.batch = append(im.batch, rec)
	if len(im.batch) >= im.batchSize {
		im.flush()
	}
	if im.total%10_000 == 0 && im.total > 0 {
		rate := float64(im.total) / time.Since(im.start).Seconds()
		log.Printf("[import] %d processed  %d imported  %d skipped  %d errors  (%.0f/s)",
			im.total, im.imported, im.skipped, im.errCount, rate)
	}
}

func (im *importer) flush() {
	if len(im.batch) == 0 {
		return
	}
	n, s, e := flushBatch(im.app, im.batch)
	im.imported += n
	im.skipped += s
	im.errCount += e
	im.batch = im.batch[:0]
}

func (im *importer) logFinal(mode string) {
	im.flush() // flush remaining batch before reporting final counts
	log.Printf("[import:%s] done: %d processed  %d imported  %d skipped  %d errors  in %s",
		mode, im.total, im.imported, im.skipped, im.errCount,
		time.Since(im.start).Round(time.Second))
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// flushBatch saves each record individually. Unique-constraint violations are
// counted as skipped (not errors) since they indicate an already-imported record.
//
// Note: a batch transaction was used previously for speed, but FileField records
// have their *filesystem.File converted to a plain string during tx.Save, so any
// retry after a transaction rollback would trigger PocketBase's "Invalid new files"
// validation. Individual saves avoid this entirely.
//
// Returns (saved, skipped, errors).
func flushBatch(app *pocketbase.PocketBase, batch []*core.Record) (saved, skipped, errors int) {
	for _, rec := range batch {
		if err := app.Save(rec); err != nil {
			msg := err.Error()
			switch {
			case isUniqueErr(msg):
				skipped++
			default:
				if errors < 3 {
					log.Printf("[import] save error (slug=%q subscene_id=%q filename=%q language=%q): %s",
						rec.GetString("slug"), rec.GetString("subscene_id"),
						rec.GetString("filename"), rec.GetString("language"), msg)
				}
				errors++
			}
		} else {
			saved++
		}
	}
	return saved, skipped, errors
}

// isUniqueErr returns true for any unique-constraint violation — either from
// PocketBase's validation layer ("Value must be unique") or from SQLite directly
// ("UNIQUE constraint failed").
func isUniqueErr(msg string) bool {
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "Value must be unique")
}

// maxContentSize is the largest file we'll attach to a subtitle record.
// ASS subtitles with embedded fonts can be tens of MB; skip content for
// anything larger so the record is still saved as metadata-only.
const maxContentSize = 20 << 20 // 20 MB

// setContent attaches the subtitle file to the record's "content" FileField.
// Files that exceed maxContentSize are skipped — the record is still saved
// without content so it remains searchable.
func setContent(rec *core.Record, sf subFile) {
	if len(sf.content) == 0 || len(sf.content) > maxContentSize {
		return
	}
	f, err := filesystem.NewFileFromBytes(sf.content, storageFilename(sf.filename))
	if err == nil {
		rec.Set("content", f)
	}
}

// storageFilename produces a PocketBase-safe filename from an arbitrary subtitle
// filename. PocketBase's FileField validation rejects generated names that still
// contain uppercase letters or hyphens in the extension segment, so we fully
// normalise both the base and the extension here before handing the name off to
// filesystem.NewFileFromBytes.
//
// The original human-readable name is preserved in the separate "filename" field.
func storageFilename(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	base := strings.ToLower(strings.TrimSuffix(filename, filepath.Ext(filename)))
	base = nonAlnumRe.ReplaceAllString(base, "_")
	base = strings.Trim(base, "_")
	if base == "" {
		base = "subtitle"
	}
	return base + ext
}

// extractSubtitlesFromZIPBytes opens an in-memory ZIP and returns subtitle files.
func extractSubtitlesFromZIPBytes(data []byte) []subFile {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil
	}
	return subtitlesFromZIPReader(zr)
}

// extractSubtitlesFromZIPPath opens a ZIP file on disk and returns subtitle files.
func extractSubtitlesFromZIPPath(path string) []subFile {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil
	}
	return extractSubtitlesFromZIPBytes(data)
}

func subtitlesFromZIPReader(zr *zip.Reader) []subFile {
	var files []subFile
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(zf.Name))
		if ext != ".srt" && ext != ".ass" && ext != ".ssa" && ext != ".sub" {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			continue
		}
		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			continue
		}
		files = append(files, subFile{
			filename: filepath.Base(zf.Name),
			format:   strings.TrimPrefix(ext, "."),
			content:  content,
		})
	}
	return files
}

// parseV2Filename extracts subscene_id, language, and HI flag from a V2 ZIP name.
//
// Pattern: {show-slug}[_HI]_{language}-{id}.zip
//
// Subscene show-slugs only use dashes, never underscores, so the FIRST
// underscore reliably separates the show-slug from the language+id portion.
// The directory slug is accepted as a parameter but unused — the filename
// alone carries all necessary information.
func parseV2Filename(_ string, filename string) (subsceneID, language string, hi bool) {
	base := strings.TrimSuffix(filename, filepath.Ext(filename))

	// Split on the first underscore: {show-slug} _ {rest}
	_, rest, ok := strings.Cut(base, "_")
	if !ok {
		return "", "", false
	}

	// Optional HI flag immediately after the first underscore.
	if strings.HasPrefix(rest, "HI_") {
		hi = true
		rest = rest[3:]
	}

	// Split rest on the last dash: {language} - {id}
	dash := strings.LastIndex(rest, "-")
	if dash < 0 {
		return "", "", false
	}
	language = rest[:dash]
	subsceneID = rest[dash+1:]
	if language == "" || subsceneID == "" {
		return "", "", false
	}
	return subsceneID, language, hi
}

// slugToTitle converts "the-dark-knight-rises" → "The Dark Knight Rises".
func slugToTitle(slug string) string {
	parts := strings.Split(slug, "-")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

func extractSlug(original string) string {
	_, rest, ok := strings.Cut(original, "/subtitles/")
	if !ok {
		return ""
	}
	if slash := strings.Index(rest, "/"); slash > 0 {
		return rest[:slash]
	}
	return rest
}

func extractIMDB(imdbURL string) string {
	m := imdbRe.FindStringSubmatch(imdbURL)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

func parseDate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse("1/2/2006 3:04 PM", s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
