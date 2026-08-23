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
	"bytes"
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
	"unicode/utf8"

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

// checkpointEvery is how many entries pass between two checkpoints of whatever
// the database writes ahead of its data file.
const checkpointEvery = 50_000

// maxPendingBytes bounds how much subtitle content a batch holds before it is
// written out. A batch is a number of entries, and an entry can hold a whole
// season, so counting entries alone does not bound memory.
const maxPendingBytes = 64 << 20

// Options configure one import run.
type Options struct {
	// Languages is the whitelist of languages whose files are extracted and
	// stored. Empty means every language. The catalogue is always loaded in full.
	Languages []string

	// DryRun reads and counts without writing anything.
	DryRun bool

	// Resume skips entries whose upload already has stored files, so a restarted
	// import does not decompress and hash what a previous run already did. It is
	// the default, because an 8-12 hour import that has to start over is not
	// resumable in any useful sense; a new dump wants it off.
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
	Deleted         int // legacy storage objects removed after their file moved
	Empty           int
	Truncated       int
	ErrorBodies     int
	NoSubtitle      int
	TooLarge        int
	Unreadable      int
	Errors          int
}

// Skipped is every entry that produced no subtitle file, for whatever reason.
func (s Stats) Skipped() int {
	return s.SkippedLanguage + s.Resumed + s.Empty + s.Truncated +
		s.ErrorBodies + s.NoSubtitle + s.TooLarge + s.Unreadable
}

func (s Stats) String() string {
	return fmt.Sprintf(
		"%d entries  %d files (%d new, %d updated, %d unchanged)  %d stored  %d migrated  %d deleted  "+
			"%d skipped (%d language, %d resumed, %d empty, %d truncated, %d error bodies, %d not subtitles, %d too large, %d unreadable)  %d errors",
		s.Entries, s.Files, s.Inserted, s.Updated, s.Unchanged, s.Stored, s.Migrated, s.Deleted,
		s.Skipped(), s.SkippedLanguage, s.Resumed, s.Empty, s.Truncated, s.ErrorBodies,
		s.NoSubtitle, s.TooLarge, s.Unreadable, s.Errors)
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
	// nextCheckpoint is the entry count at which the log is next folded back in.
	nextCheckpoint int
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
		st:             st,
		stor:           stor,
		opts:           opts,
		allow:          allow,
		start:          time.Now(),
		nextReport:     progressEvery,
		nextCheckpoint: checkpointEvery,
	}
}

func (i *Ingester) Stats() Stats { return i.stats }

// ─── pass 1: the catalogue ───────────────────────────────────────────────────

// CatalogueFormat is how a dump's catalogue is written: the V2 archive ships a
// SQL dump, the V1 one a metadata.json. Both carry the same facts and become the
// same rows.
type CatalogueFormat int

const (
	CatalogueSQL CatalogueFormat = iota
	CatalogueJSON
)

// LoadCatalogue reads a dump's catalogue into `uploads`.
//
// The whitelist is deliberately ignored here: the catalogue is what makes the
// data model complete, and loading it in full is what lets an operator widen the
// whitelist later without re-reading the metadata.
func (i *Ingester) LoadCatalogue(ctx context.Context, r io.Reader, format CatalogueFormat) (catalogue.Result, error) {
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

	read := catalogue.Read
	if format == CatalogueJSON {
		read = catalogue.ReadJSON
	}

	result, err := read(r, func(row catalogue.Upload) error {
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
		FilePath:   text(row.FilePath),
		Slug:       row.Slug,
		Title:      text(row.Title),
		ImdbID:     row.ImdbID,
		Language:   row.Language,
		HI:         row.HI,
		Year:       row.Year,
		Author:     text(row.Author),
		AuthorID:   row.AuthorID,
		Comment:    text(row.Comment),
		Releases:   text(string(releases)),
		UploadedAt: row.UploadedAt,
	}, true
}

// text keeps a value a database will accept. PostgreSQL and MySQL refuse a row
// whose text is not valid UTF-8 — the whole row, not the offending field — so a
// byte the dump got from somewhere else must not be able to cost a subtitle, or
// on the catalogue pass, the entire import.
func text(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "\uFFFD")
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
	var pending int
	seen := make(map[string]struct{}, len(entries))

	flush := func() error {
		if err := i.writeUploads(ctx, invented); err != nil {
			return err
		}
		invented = invented[:0]

		if len(candidates) > 0 {
			resolveFileIDs(candidates, existing)
			if err := i.writeFiles(ctx, candidates); err != nil {
				return err
			}
			candidates = candidates[:0]
			pending = 0
		}
		return nil
	}

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
			pending += len(f.Content)
		}

		if pending >= maxPendingBytes {
			if err := flush(); err != nil {
				return err
			}
		}
	}

	if err := flush(); err != nil {
		return err
	}

	i.report()
	i.checkpoint(ctx)
	return nil
}

// checkpoint keeps the database's write-ahead log from growing for the whole
// length of an import. A checkpoint that cannot run right now is not a failure —
// the next one will get it — so it is reported and the import carries on.
func (i *Ingester) checkpoint(ctx context.Context) {
	if i.opts.DryRun || i.stats.Entries < i.nextCheckpoint {
		return
	}
	i.nextCheckpoint = i.stats.Entries + checkpointEvery
	if err := i.st.Checkpoint(ctx); err != nil {
		log.Printf("[import] %v", err)
	}
}

// resolveUploads finds the catalogue row for each entry, keyed by the entry's
// path. The Subscene id in the file name is tried as well, because an archive
// whose paths carry a different prefix than the catalogue's would otherwise look
// like a dump with no metadata at all.
func (i *Ingester) resolveUploads(ctx context.Context, entries []archive.Entry) (map[string]store.Upload, error) {
	paths := make([]string, 0, len(entries)*2)
	ids := make([]string, 0, len(entries))
	idOf := make(map[string]string, len(entries))

	for _, e := range entries {
		// The archive names an entry from the root of the dump; the V2 catalogue
		// records it from the directory the subtitles live in, and the V1 one
		// records only the download's name. All three are tried.
		paths = append(paths, e.Name(), workRelativePath(e.Name()), path.Base(e.Name()))
		if id := catalogue.SubsceneIDFromPath(e.Name()); id != "" {
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
		if u, ok := byPath[workRelativePath(e.Name())]; ok {
			out[e.Name()] = u
			continue
		}
		if u, ok := byPath[path.Base(e.Name())]; ok {
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
			Filename:    text(c.file.Name),
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
			i.stats.Deleted++
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
	if err := i.stor.Put(ctx, c.key, bytes.NewReader(c.file.Content), int64(len(c.file.Content))); err != nil {
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

	data, err := io.ReadAll(io.LimitReader(rc, subfile.MaxEntrySize+1))
	if err != nil {
		return nil, subfile.ReasonUnreadable
	}
	if len(data) > subfile.MaxEntrySize {
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

	id := catalogue.SubsceneIDFromPath(name)
	if id == "" {
		// The archive cuts a name off at the filesystem's path limit, taking the
		// upload id with it — and sometimes half the extension and half the
		// language too. The subtitle is still there, so the upload gets an id
		// derived from its path: stable, so a re-import finds the same row.
		//
		// Only for a file that belongs to the work its directory names. The
		// archive also holds a couple of loose text files at its root, and those
		// are not uploads at all.
		if !belongsToDirectory(stem, slug) {
			return store.Upload{}
		}
		id = derivedUploadID(name)
	}

	// `<slug>_<language>-<id>` and `<slug>_HI_<language>-<id>`: what is left after
	// the slug and the id is the language. The slug is cut off the front by how
	// much of it the name actually repeats, because a truncated name stops
	// partway through it.
	rest := stem[sharedPrefix(stem, slug):]
	rest = strings.TrimLeft(rest, "_-")
	rest = strings.TrimSuffix(rest, "-"+id)

	hi := strings.HasPrefix(strings.ToUpper(rest), "HI_")
	if hi {
		rest = rest[3:]
	}

	subsceneID := id
	if strings.HasPrefix(id, derivedIDPrefix) {
		// Nothing outside subsarr should read a derived id as a Subscene one.
		subsceneID = ""
	}

	return store.Upload{
		ID:         id,
		SubsceneID: subsceneID,
		FilePath:   name,
		Slug:       slug,
		Title:      title.FromSlug(slug),
		Language:   lang.Canonical(rest),
		HI:         hi || strings.Contains(strings.ToUpper(base), "_HI_"),
		Year:       title.YearFromSlug(slug),
		Releases:   "[]",
	}
}

// workRelativePath is an entry's path from the work's own directory: the last two
// components, `<slug>/<file>`. It is what the V2 catalogue records, where the
// archive names the same entry from the root of the dump.
func workRelativePath(name string) string {
	dir := path.Dir(name)
	if dir == "." || dir == "/" {
		return name
	}
	return path.Base(dir) + "/" + path.Base(name)
}

// derivedIDPrefix marks an upload id subsarr made up because the archive's file
// name did not carry one.
const derivedIDPrefix = "p"

// minSharedPrefix is how much of its directory's name a file has to repeat before
// it is taken to belong to that work. Subscene names every file after the work's
// slug, so a file that does not is not one of its subtitles.
const minSharedPrefix = 8

// derivedUploadID is a stable id for an entry whose name lost its own, so that
// re-importing the same archive finds the same row rather than making a new one.
func derivedUploadID(name string) string {
	sum := sha256.Sum256([]byte(name))
	return derivedIDPrefix + hex.EncodeToString(sum[:])[:16]
}

// belongsToDirectory reports whether a file name looks like one of the work its
// directory names. Either can be cut short by the archive, so it is the shared
// start that counts.
func belongsToDirectory(stem, slug string) bool {
	if slug == "" || stem == "" {
		return false
	}
	shared := sharedPrefix(stem, slug)
	return shared >= minSharedPrefix || shared == len(slug)
}

// sharedPrefix is how many bytes two names begin with in common.
func sharedPrefix(a, b string) int {
	a, b = strings.ToLower(a), strings.ToLower(b)
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}
