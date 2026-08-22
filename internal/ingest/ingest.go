// Package ingest turns the Subscene archive into rows and stored files.
//
// The import is two passes over the same stream. Pass one loads the catalogue —
// one row per Subscene upload, with the metadata the file names never carried.
// Pass two walks the archive entries, extracts every subtitle file it can read,
// and stores it under a content-addressed key.
//
// Both passes are idempotent and lookup-first: an upload that is already known is
// updated in place, a file that is already stored keeps its id, and an object
// whose content is already in storage is not written again. Re-running an import
// after an interruption, a new dump or a bug fix therefore duplicates nothing.
package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slimcdk/subsarr/internal/archive"
	"github.com/slimcdk/subsarr/internal/catalogue"
	"github.com/slimcdk/subsarr/internal/lang"
	"github.com/slimcdk/subsarr/internal/storage"
	"github.com/slimcdk/subsarr/internal/store"
	"github.com/slimcdk/subsarr/internal/subfile"
	"github.com/slimcdk/subsarr/internal/title"
)

// progressEvery is how often a running import reports itself. The full archive
// holds about 2.5 million entries, so this is roughly 250 lines for a whole run.
const progressEvery = 10_000

// Options configure one import run.
type Options struct {
	// Languages is the whitelist of languages whose files are extracted and
	// stored. Empty means every language. The catalogue is always loaded in full.
	Languages []string

	// DryRun reads and counts without writing anything.
	DryRun bool

	// Resume skips entries whose upload already has stored files, so a restarted
	// import does not decompress and hash what a previous run already did.
	Resume bool

	// Batch is how many archive entries are processed per transaction.
	Batch int

	// Limit stops after this many entries (0 = all).
	Limit int
}

// Stats is the running account of an import. Every entry that produces no row is
// counted under the reason it produced none, so a missing subtitle can always be
// explained.
type Stats struct {
	CatalogueRows int

	Entries   int
	Files     int
	Inserted  int
	Updated   int
	Unchanged int
	Stored    int
	Migrated  int // stored files moved from a legacy key to a content-addressed one

	SkippedLanguage int
	Resumed         int // entries skipped because a previous run already stored them
	Empty           int
	Truncated       int
	ErrorBodies     int
	NoSubtitle      int
	TooLarge        int
	Unreadable      int
	Errors          int
}

func (s Stats) String() string {
	return fmt.Sprintf(
		"%d entries  %d files (%d new, %d updated, %d unchanged)  %d stored  %d migrated  "+
			"skipped: %d language, %d resumed, %d empty, %d truncated, %d error bodies, %d not subtitles, %d too large, %d unreadable, %d errors",
		s.Entries, s.Files, s.Inserted, s.Updated, s.Unchanged, s.Stored, s.Migrated,
		s.SkippedLanguage, s.Resumed, s.Empty, s.Truncated, s.ErrorBodies, s.NoSubtitle, s.TooLarge, s.Unreadable, s.Errors)
}

// Ingester runs an import against a store and a storage backend.
type Ingester struct {
	st    store.Store
	stor  storage.Store
	opts  Options
	allow map[string]struct{} // nil when every language is allowed

	stats Stats
	start time.Time
	// nextReport is the entry count at which the next progress line is due.
	nextReport int
}

func New(st store.Store, stor storage.Store, opts Options) *Ingester {
	if opts.Batch <= 0 {
		opts.Batch = 500
	}

	var allow map[string]struct{}
	if len(opts.Languages) > 0 {
		allow = make(map[string]struct{}, len(opts.Languages))
		for _, l := range opts.Languages {
			allow[lang.Canonical(l)] = struct{}{}
		}
	}

	return &Ingester{
		st:         st,
		stor:       stor,
		opts:       opts,
		allow:      allow,
		start:      time.Now(),
		nextReport: progressEvery,
	}
}

func (i *Ingester) Stats() Stats { return i.stats }

// ─── pass 1: the catalogue ───────────────────────────────────────────────────

// LoadCatalogue reads the archive's SQL dump into `uploads`.
//
// The whitelist is deliberately ignored here: the catalogue is what makes the
// data model complete, and loading it in full is what lets an operator widen the
// whitelist later without re-reading the metadata.
func (i *Ingester) LoadCatalogue(ctx context.Context, r io.Reader) (catalogue.Result, error) {
	batch := make([]store.Upload, 0, i.opts.Batch)

	flush := func() error {
		if len(batch) == 0 || i.opts.DryRun {
			batch = batch[:0]
			return nil
		}
		if err := i.st.UpsertUploads(ctx, batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	result, err := catalogue.Read(r, func(row catalogue.Upload) error {
		upload, ok := uploadFromCatalogue(row)
		if !ok {
			return nil
		}
		batch = append(batch, upload)
		i.stats.CatalogueRows++

		if i.stats.CatalogueRows%progressEvery == 0 {
			log.Printf("[import] catalogue: %d rows (%s)", i.stats.CatalogueRows, i.rate(i.stats.CatalogueRows))
		}
		if len(batch) >= i.opts.Batch {
			return flush()
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	return result, flush()
}

// uploadFromCatalogue converts one catalogue row into a row of `uploads`. A row
// with no id cannot be joined to a file and is dropped.
func uploadFromCatalogue(row catalogue.Upload) (store.Upload, bool) {
	if row.SubsceneID == "" {
		return store.Upload{}, false
	}
	releases, _ := json.Marshal(nonNil(row.Releases))
	return store.Upload{
		ID:         row.SubsceneID,
		SubsceneID: row.SubsceneID,
		FilePath:   row.FilePath,
		Slug:       row.Slug,
		Title:      row.Title,
		ImdbID:     row.ImdbID,
		Language:   row.Language,
		HI:         row.HI,
		Year:       row.Year,
		Author:     row.Author,
		AuthorID:   row.AuthorID,
		Comment:    row.Comment,
		Releases:   string(releases),
		UploadedAt: row.UploadedAt,
	}, true
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// ─── pass 2: the files ───────────────────────────────────────────────────────

// Run streams a source and ingests every entry it holds.
func (i *Ingester) Run(ctx context.Context, src archive.Source) error {
	batch := make([]archive.Entry, 0, i.opts.Batch)

	err := src.Each(func(e archive.Entry) error {
		if i.opts.Limit > 0 && i.stats.Entries >= i.opts.Limit {
			return archive.ErrStop
		}
		i.stats.Entries++

		batch = append(batch, e)
		if len(batch) < i.opts.Batch {
			return nil
		}
		err := i.processBatch(ctx, batch)
		batch = batch[:0]
		return err
	})
	if err != nil {
		return err
	}
	return i.processBatch(ctx, batch)
}

// candidate is one subtitle file about to be written.
type candidate struct {
	upload    store.Upload
	file      subfile.File
	hash      string
	key       string
	legacyKey string // the object this file used to live under, to be deleted
	id        string
	existing  bool
	changed   bool
}

func (i *Ingester) processBatch(ctx context.Context, entries []archive.Entry) error {
	if len(entries) == 0 {
		return nil
	}

	uploads, err := i.resolveUploads(ctx, entries)
	if err != nil {
		return err
	}
	existing, err := i.existingFiles(ctx, uploads)
	if err != nil {
		return err
	}

	// Uploads invented for entries the catalogue does not cover. They are written
	// before their files so the join has something to point at.
	var invented []store.Upload
	var candidates []candidate
	seen := make(map[string]struct{}, len(entries))

	for _, entry := range entries {
		upload, ok := uploads[entry.Name()]
		if !ok {
			upload = uploadFromFilename(entry.Name())
			if upload.ID == "" {
				i.stats.NoSubtitle++
				continue
			}
			if _, dup := seen[upload.ID]; !dup {
				invented = append(invented, upload)
				seen[upload.ID] = struct{}{}
			}
		}

		if !i.allowed(upload.Language) {
			i.stats.SkippedLanguage++
			continue
		}

		// Resuming: this upload already has stored files, so a previous run got
		// this far. Decompressing and hashing it again would change nothing.
		if i.opts.Resume && len(existing[upload.ID]) > 0 {
			i.stats.Resumed++
			continue
		}

		files, reason := i.read(entry)
		if reason != subfile.ReasonNone {
			i.count(reason)
			continue
		}
		for _, f := range files {
			sum := sha256.Sum256(f.Content)
			hash := hex.EncodeToString(sum[:])
			candidates = append(candidates, candidate{
				upload: upload,
				file:   f,
				hash:   hash,
				key:    ContentKey(hash, f.Name),
			})
		}
	}

	if len(candidates) == 0 {
		i.report()
		return i.writeUploads(ctx, invented)
	}

	resolveFileIDs(candidates, existing)

	if err := i.writeUploads(ctx, invented); err != nil {
		return err
	}
	if err := i.writeFiles(ctx, candidates); err != nil {
		return err
	}

	i.report()
	return nil
}

// resolveUploads finds the catalogue row for each entry, keyed by the entry's
// path. The Subscene id in the file name is tried as well, because an archive
// whose paths carry a different prefix than the catalogue's would otherwise look
// like a dump with no metadata at all.
func (i *Ingester) resolveUploads(ctx context.Context, entries []archive.Entry) (map[string]store.Upload, error) {
	paths := make([]string, 0, len(entries))
	ids := make([]string, 0, len(entries))
	idOf := make(map[string]string, len(entries))

	for _, e := range entries {
		paths = append(paths, e.Name())
		if id := subsceneIDFromName(e.Name()); id != "" {
			ids = append(ids, id)
			idOf[e.Name()] = id
		}
	}

	byPath, err := i.st.UploadsByPath(ctx, paths)
	if err != nil {
		return nil, fmt.Errorf("resolve uploads by path: %w", err)
	}
	byID, err := i.st.UploadsByID(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("resolve uploads by id: %w", err)
	}

	out := make(map[string]store.Upload, len(entries))
	for _, e := range entries {
		if u, ok := byPath[e.Name()]; ok {
			out[e.Name()] = u
			continue
		}
		if u, ok := byID[idOf[e.Name()]]; ok {
			out[e.Name()] = u
		}
	}
	return out, nil
}

// existingFiles fetches the files already stored for the batch's uploads, in one
// query. It answers two questions at once: whether an entry can be skipped on a
// resumed run, and which id a file it holds should keep.
func (i *Ingester) existingFiles(ctx context.Context, uploads map[string]store.Upload) (map[string][]store.File, error) {
	ids := make([]string, 0, len(uploads))
	seen := make(map[string]struct{}, len(uploads))
	for _, u := range uploads {
		if _, dup := seen[u.ID]; dup {
			continue
		}
		seen[u.ID] = struct{}{}
		ids = append(ids, u.ID)
	}

	files, err := i.st.FilesByUpload(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("look up existing files: %w", err)
	}
	return files, nil
}

// resolveFileIDs decides, for each candidate, whether it is a file the store
// already has. A known file keeps its id — that id is the download URL an
// operator's Bazarr already holds — and its old storage object is remembered so
// it can be removed once the new content-addressed one is committed.
func resolveFileIDs(candidates []candidate, existing map[string][]store.File) {
	for n := range candidates {
		c := &candidates[n]
		c.id = uuid.NewString()
		for _, f := range existing[c.upload.ID] {
			if f.ContentHash != c.hash {
				continue
			}
			c.id = f.ID
			c.existing = true
			c.changed = f.ContentKey != c.key || f.Filename != c.file.Name || f.Format != c.file.Format
			if f.ContentKey != "" && f.ContentKey != c.key {
				c.legacyKey = f.ContentKey
			}
			break
		}
	}
}

func (i *Ingester) writeUploads(ctx context.Context, uploads []store.Upload) error {
	if len(uploads) == 0 || i.opts.DryRun {
		return nil
	}
	if err := i.st.UpsertUploads(ctx, uploads); err != nil {
		return fmt.Errorf("write uploads: %w", err)
	}
	return nil
}

// writeFiles stores the content and then the rows. Content first: a row that
// points at an object that is not there yet would answer a download with a 404,
// while an object with no row is invisible and is cleaned up by the next prune.
func (i *Ingester) writeFiles(ctx context.Context, candidates []candidate) error {
	rows := make([]store.IngestFile, 0, len(candidates))
	var legacy []string

	for _, c := range candidates {
		i.stats.Files++
		switch {
		case !c.existing:
			i.stats.Inserted++
		case c.changed:
			i.stats.Updated++
		default:
			i.stats.Unchanged++
		}

		if !i.opts.DryRun {
			stored, err := i.store(ctx, c)
			if err != nil {
				i.stats.Errors++
				continue
			}
			if stored {
				i.stats.Stored++
			}
		}
		if c.legacyKey != "" {
			legacy = append(legacy, c.legacyKey)
		}

		rows = append(rows, store.IngestFile{
			ID:          c.id,
			UploadID:    c.upload.ID,
			Filename:    c.file.Name,
			Format:      c.file.Format,
			ContentHash: c.hash,
			ContentKey:  c.key,
			Size:        int64(len(c.file.Content)),
		})
	}

	if i.opts.DryRun {
		return nil
	}
	if err := i.st.IngestFiles(ctx, rows); err != nil {
		// One bad row must not cost the batch, so the batch is retried row by row
		// to find it and count it.
		i.stats.Errors += i.ingestIndividually(ctx, rows)
	}

	// Only now that the rows are committed is the old object unreferenced.
	for _, key := range legacy {
		if err := i.stor.Delete(ctx, key); err == nil {
			i.stats.Migrated++
		}
	}
	return nil
}

func (i *Ingester) ingestIndividually(ctx context.Context, rows []store.IngestFile) int {
	var failed int
	for _, row := range rows {
		if err := i.st.IngestFiles(ctx, []store.IngestFile{row}); err != nil {
			log.Printf("[import] file %s (upload %s): %v", row.Filename, row.UploadID, err)
			failed++
		}
	}
	return failed
}

// store writes the content unless the content-addressed object is already there,
// which is what makes a second import cheap and identical subtitles share one
// object.
func (i *Ingester) store(ctx context.Context, c candidate) (bool, error) {
	exists, err := i.stor.Exists(ctx, c.key)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	if err := i.stor.Put(ctx, c.key, strings.NewReader(string(c.file.Content)), int64(len(c.file.Content))); err != nil {
		return false, fmt.Errorf("store %s: %w", c.key, err)
	}
	return true, nil
}

// read opens an entry and extracts the subtitle files it holds.
func (i *Ingester) read(entry archive.Entry) ([]subfile.File, subfile.Reason) {
	rc, err := entry.Open()
	if err != nil {
		return nil, subfile.ReasonUnreadable
	}
	defer rc.Close()

	data, err := io.ReadAll(io.LimitReader(rc, subfile.MaxFileSize+1))
	if err != nil {
		return nil, subfile.ReasonUnreadable
	}
	if len(data) > subfile.MaxFileSize {
		return nil, subfile.ReasonTooLarge
	}
	return subfile.Extract(entry.Name(), data)
}

func (i *Ingester) allowed(language string) bool {
	if i.allow == nil {
		return true
	}
	if language == "" {
		// An upload whose language is unknown is only extracted when the operator
		// asked for unknown languages by name.
		_, ok := i.allow["unknown"]
		return ok
	}
	_, ok := i.allow[language]
	return ok
}

func (i *Ingester) count(reason subfile.Reason) {
	switch reason {
	case subfile.ReasonEmpty:
		i.stats.Empty++
	case subfile.ReasonTruncated:
		i.stats.Truncated++
	case subfile.ReasonErrorBody:
		i.stats.ErrorBodies++
	case subfile.ReasonTooLarge:
		i.stats.TooLarge++
	case subfile.ReasonUnreadable:
		i.stats.Unreadable++
	default:
		i.stats.NoSubtitle++
	}
}

func (i *Ingester) report() {
	if i.stats.Entries < i.nextReport {
		return
	}
	i.nextReport = i.stats.Entries + progressEvery
	log.Printf("[import] %s (%s)", i.stats, i.rate(i.stats.Entries))
}

func (i *Ingester) rate(n int) string {
	elapsed := time.Since(i.start).Seconds()
	if elapsed <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f/s", float64(n)/elapsed)
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// ContentKey is the storage key for a piece of content. Addressing by hash means
// the same subtitle uploaded under several ids is stored once, and that a
// re-import can tell what is already stored without reading it.
func ContentKey(hash, filename string) string {
	ext := strings.ToLower(path.Ext(filename))
	if _, ok := subfile.Format(filename); !ok {
		ext = ""
	}
	return "content/" + hash[0:2] + "/" + hash[2:4] + "/" + hash + ext
}

// uploadFromFilename invents a catalogue row for an entry the catalogue does not
// cover — about 350 of the archive's two and a half million files.
func uploadFromFilename(name string) store.Upload {
	base := path.Base(name)
	stem := strings.TrimSuffix(base, path.Ext(base))
	slug := path.Base(path.Dir(name))
	if slug == "." || slug == "/" {
		slug = ""
	}

	id := subsceneIDFromName(name)
	if id == "" {
		return store.Upload{}
	}

	// `<slug>_<language>-<id>` and `<slug>_HI_<language>-<id>`: what is left after
	// the slug prefix and the id suffix is the language.
	rest := strings.TrimSuffix(stem, "-"+id)
	rest = strings.TrimPrefix(rest, slug)
	rest = strings.TrimPrefix(rest, "_")

	hi := strings.HasPrefix(strings.ToUpper(rest), "HI_")
	if hi {
		rest = rest[3:]
	}

	return store.Upload{
		ID:         id,
		SubsceneID: id,
		FilePath:   name,
		Slug:       slug,
		Title:      title.FromSlug(slug),
		Language:   lang.Canonical(rest),
		HI:         hi || strings.Contains(strings.ToUpper(base), "_HI_"),
		Year:       title.YearFromSlug(slug),
		Releases:   "[]",
	}
}

// subsceneIDFromName reads the upload id off the end of an archive file name.
func subsceneIDFromName(name string) string {
	base := path.Base(name)
	stem := strings.TrimSuffix(base, path.Ext(base))

	dash := strings.LastIndex(stem, "-")
	if dash < 0 || dash == len(stem)-1 {
		return ""
	}
	id := stem[dash+1:]
	for _, r := range id {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return id
}
