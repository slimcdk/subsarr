// Package importcmd provides a CLI command to import Subscene subtitle dumps.
//
// # One-shot streaming from archive (no extraction to disk)
//
//	./subsarr import-dump --archive "/path/to/Subscene V2.7z.001"
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
	"context"
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
	"github.com/google/uuid"
	"github.com/slimcdk/subsarr/internal/config"
	"github.com/slimcdk/subsarr/internal/database"
	"github.com/slimcdk/subsarr/internal/storage"
	"github.com/slimcdk/subsarr/internal/store"
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

// NewCommand creates the import-dump cobra command.
func NewCommand(cfg config.Config) *cobra.Command {
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
			db, err := database.Open(cfg.DBDriver, cfg.DBDSN)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			if err := database.Migrate(db, cfg.DBDriver); err != nil {
				return fmt.Errorf("migrate: %w", err)
			}

			st, err := store.New(db, cfg.DBDriver)
			if err != nil {
				return fmt.Errorf("store: %w", err)
			}

			stor, err := storage.New(cfg)
			if err != nil {
				return fmt.Errorf("storage: %w", err)
			}

			archive, _ := cmd.Flags().GetString("archive")
			metaPath, _ := cmd.Flags().GetString("metadata")
			subsDir, _ := cmd.Flags().GetString("subtitles")
			filesDB, _ := cmd.Flags().GetString("files-db")
			batchSize, _ := cmd.Flags().GetInt("batch")
			limit, _ := cmd.Flags().GetInt("limit")

			switch {
			case archive != "":
				return runFromArchive(st, stor, archive, batchSize, limit)
			case filesDB != "":
				return runV2(st, stor, filesDB, batchSize, limit)
			case metaPath != "":
				return runV1(st, stor, metaPath, subsDir, batchSize, limit)
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

	return cmd
}

// ─── streaming from archive ───────────────────────────────────────────────────

func runFromArchive(st store.Store, stor storage.Store, archivePath string, batchSize, limit int) error {
	log.Printf("[import] opening archive %s …", archivePath)
	r, err := sevenzip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer r.Close()

	log.Printf("[import] archive has %d entries across volumes: %v", len(r.File), r.Volumes())

	format := detectArchiveFormat(r)
	log.Printf("[import] detected format: %s", format)

	switch format {
	case "v1":
		return streamV1(st, stor, r, batchSize, limit)
	case "v2":
		return streamV2(st, stor, r, batchSize, limit)
	default:
		return fmt.Errorf("unrecognised archive layout — expected metadata.json (V1) or 'Subscene Files DB/' (V2)")
	}
}

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

func streamV2(st store.Store, stor storage.Store, r *sevenzip.ReadCloser, batchSize, limit int) error {
	imp := newImporter(st, stor, batchSize)
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
			files = []subFile{{filename: filename, format: "zip"}}
		}

		title := slugToTitle(slug)
		for _, sf := range files {
			sub := &store.Subtitle{
				ID:         uuid.NewString(),
				SubsceneID: subsceneID,
				Title:      title,
				Slug:       slug,
				Language:   language,
				HI:         hi,
				Filename:   sf.filename,
				Format:     sf.format,
				Downloads:  0,
			}
			imp.storeContent(sub, sf)
			imp.add(sub)
		}

		if i%5_000 == 0 {
			log.Printf("[import] scanned %d/%d archive entries", i, total)
		}
	}

	imp.logFinal("V2 stream")
	return nil
}

func streamV1(st store.Store, stor storage.Store, r *sevenzip.ReadCloser, batchSize, limit int) error {
	log.Printf("[import] V1 pass 1/2: loading metadata.json …")
	meta, err := loadV1Metadata(r)
	if err != nil {
		return err
	}
	log.Printf("[import] loaded %d metadata entries", len(meta))

	imp := newImporter(st, stor, batchSize)
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

		zipName := filepath.Base(f.Name)
		entry, ok := meta[zipName]
		if !ok {
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

		releasesJSON, _ := json.Marshal(entry.Releases)

		for _, sf := range files {
			sub := &store.Subtitle{
				ID:         uuid.NewString(),
				SubsceneID: entry.SubsceneID,
				Title:      title,
				Slug:       slug,
				ImdbID:     imdbID,
				Language:   entry.Language,
				HI:         hi,
				Author:     entry.Author,
				Releases:   string(releasesJSON),
				Comment:    entry.Comment,
				Filename:   sf.filename,
				Format:     sf.format,
				Downloads:  0,
			}
			if !uploadedAt.IsZero() {
				sub.UploadedAt = uploadedAt.Format(time.RFC3339)
			}
			imp.storeContent(sub, sf)
			imp.add(sub)
		}
	}

	imp.logFinal("V1 stream")
	return nil
}

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
		if _, err := dec.Token(); err != nil {
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

func readArchiveFile(f *sevenzip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// ─── manual V1 (extracted) ────────────────────────────────────────────────────

func runV1(st store.Store, stor storage.Store, metaPath, subsDir string, batchSize, limit int) error {
	f, err := os.Open(metaPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", metaPath, err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	if _, err := dec.Token(); err != nil {
		return fmt.Errorf("metadata.json does not start with '[': %w", err)
	}

	imp := newImporter(st, stor, batchSize)
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

		releasesJSON, _ := json.Marshal(entry.Releases)

		for _, sf := range files {
			sub := &store.Subtitle{
				ID:         uuid.NewString(),
				SubsceneID: entry.SubsceneID,
				Title:      title,
				Slug:       slug,
				ImdbID:     imdbID,
				Language:   entry.Language,
				HI:         hi,
				Author:     entry.Author,
				Releases:   string(releasesJSON),
				Comment:    entry.Comment,
				Filename:   sf.filename,
				Format:     sf.format,
				Downloads:  0,
			}
			if !uploadedAt.IsZero() {
				sub.UploadedAt = uploadedAt.Format(time.RFC3339)
			}
			imp.storeContent(sub, sf)
			imp.add(sub)
		}
	}

	imp.logFinal("V1")
	return nil
}

// ─── manual V2 (extracted) ────────────────────────────────────────────────────

func runV2(st store.Store, stor storage.Store, filesDBDir string, batchSize, limit int) error {
	imp := newImporter(st, stor, batchSize)
	defer imp.flush()

	err := filepath.WalkDir(filesDBDir, func(path string, d fs.DirEntry, walkErr error) error {
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
			sub := &store.Subtitle{
				ID:         uuid.NewString(),
				SubsceneID: subsceneID,
				Title:      title,
				Slug:       slug,
				Language:   language,
				HI:         hi,
				Filename:   sf.filename,
				Format:     sf.format,
				Downloads:  0,
			}
			imp.storeContent(sub, sf)
			imp.add(sub)
		}
		return nil
	})

	imp.logFinal("V2")
	return err
}

// ─── shared importer ─────────────────────────────────────────────────────────

type importer struct {
	st        store.Store
	stor      storage.Store
	batchSize int
	batch     []*store.Subtitle
	total     int
	imported  int
	skipped   int
	errCount  int
	start     time.Time
}

func newImporter(st store.Store, stor storage.Store, batchSize int) *importer {
	return &importer{st: st, stor: stor, batchSize: batchSize, start: time.Now()}
}

func (im *importer) add(sub *store.Subtitle) {
	im.batch = append(im.batch, sub)
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
	n, s, e := im.st.InsertSubtitleBatch(context.Background(), im.batch)
	im.imported += n
	im.skipped += s
	im.errCount += e
	im.batch = im.batch[:0]
}

func (im *importer) logFinal(mode string) {
	im.flush()
	log.Printf("[import:%s] done: %d processed  %d imported  %d skipped  %d errors  in %s",
		mode, im.total, im.imported, im.skipped, im.errCount,
		time.Since(im.start).Round(time.Second))
}

// maxContentSize is the largest file we'll store.
const maxContentSize = 20 << 20 // 20 MB

// storeContent uploads the subtitle file content to the storage backend
// and sets the ContentKey on the subtitle record.
func (im *importer) storeContent(sub *store.Subtitle, sf subFile) {
	if len(sf.content) == 0 || len(sf.content) > maxContentSize {
		return
	}
	key := fmt.Sprintf("subtitles/%s/%s", sub.ID, storageFilename(sf.filename))
	err := im.stor.Put(context.Background(), key, bytes.NewReader(sf.content), int64(len(sf.content)))
	if err == nil {
		sub.ContentKey = key
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

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

func extractSubtitlesFromZIPBytes(data []byte) []subFile {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil
	}
	return subtitlesFromZIPReader(zr)
}

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

func parseV2Filename(_ string, filename string) (subsceneID, language string, hi bool) {
	base := strings.TrimSuffix(filename, filepath.Ext(filename))

	_, rest, ok := strings.Cut(base, "_")
	if !ok {
		return "", "", false
	}

	if strings.HasPrefix(rest, "HI_") {
		hi = true
		rest = rest[3:]
	}

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
