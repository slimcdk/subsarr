package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/slimcdk/subsarr/internal/title"
)

type sqlStore struct {
	db *sql.DB
	d  dialect
}

func (s *sqlStore) DB() *sql.DB { return s.db }

// subtitleColumns is the projection every read returns, in scan order.
const subtitleColumns = `f.id, f.upload_id, f.filename, f.format, f.content_key, f.content_hash, f.downloads,
	u.subscene_id, u.title, u.slug, u.imdb_id, u.language, u.hi, u.author, u.releases, u.comment, u.year, u.uploaded_at`

func scanSubtitle(scan func(...any) error) (Subtitle, error) {
	var sub Subtitle
	err := scan(
		&sub.ID, &sub.UploadID, &sub.Filename, &sub.Format, &sub.ContentKey, &sub.ContentHash, &sub.Downloads,
		&sub.SubsceneID, &sub.Title, &sub.Slug, &sub.ImdbID, &sub.Language, &sub.HI, &sub.Author,
		&sub.Releases, &sub.Comment, &sub.Year, &sub.UploadedAt,
	)
	return sub, err
}

// ─── reading ─────────────────────────────────────────────────────────────────

func (s *sqlStore) GetSubtitle(ctx context.Context, id string) (*Subtitle, error) {
	b := newBuilder(s.d)
	b.write("SELECT " + subtitleColumns + " FROM files f JOIN uploads u ON u.id = f.upload_id WHERE f.id = ")
	b.write(b.bind(id))

	sub, err := scanSubtitle(s.db.QueryRowContext(ctx, b.String(), b.args...).Scan)
	if err != nil {
		return nil, err
	}
	return &sub, nil
}

func (s *sqlStore) IncrementDownloads(ctx context.Context, id string) error {
	b := newBuilder(s.d)
	b.write("UPDATE files SET downloads = downloads + 1 WHERE id = ")
	b.write(b.bind(id))
	_, err := s.db.ExecContext(ctx, b.String(), b.args...)
	return err
}

// ListLanguages counts the languages that actually have subtitle files. An
// upload whose files were never stored — filtered out by a language whitelist,
// or holding nothing readable — must not be advertised as available.
func (s *sqlStore) ListLanguages(ctx context.Context) ([]LanguageCount, error) {
	const q = `SELECT u.language, COUNT(*) AS n
		FROM files f JOIN uploads u ON u.id = f.upload_id
		WHERE u.language <> ''
		GROUP BY u.language
		ORDER BY n DESC, u.language ASC`

	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []LanguageCount
	for rows.Next() {
		var lc LanguageCount
		if err := rows.Scan(&lc.Language, &lc.Count); err != nil {
			return nil, err
		}
		out = append(out, lc)
	}
	return out, rows.Err()
}

// HasIMDBIDs reports whether the catalogue carries IMDB ids at all, so that
// /info can answer truthfully and an IMDB search can short-circuit instead of
// scanning for something that cannot be there.
func (s *sqlStore) HasIMDBIDs(ctx context.Context) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, "SELECT 1 FROM uploads WHERE imdb_id <> '' LIMIT 1").Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (s *sqlStore) CountUploads(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM uploads").Scan(&n)
	return n, err
}

// CatalogueLanguages returns every language present in the catalogue, whether or
// not its files were stored. It is what a language whitelist is validated
// against.
func (s *sqlStore) CatalogueLanguages(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT DISTINCT language FROM uploads WHERE language <> ''")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ─── search ──────────────────────────────────────────────────────────────────

// SearchSubtitles answers one Bazarr query.
//
// Without a free-text query this is a single indexed seek. With one, the query is
// resolved through the title index first — all words, and only if that finds
// nothing, a substring match — because a title maps to a few dozen uploads and
// the alternative is scanning every upload in the requested language.
func (s *sqlStore) SearchSubtitles(ctx context.Context, p SearchParams) ([]Subtitle, int, error) {
	if p.Query == "" {
		return s.search(ctx, p, nil)
	}

	for _, mode := range []matchMode{matchAllWords, matchSubstring} {
		subs, total, err := s.search(ctx, p, &mode)
		if err != nil {
			return nil, 0, err
		}
		if total > 0 {
			return subs, total, nil
		}
	}
	return nil, 0, nil
}

// buildSearch assembles one statement. Arguments are bound in the order they
// appear in the SQL, because a `?` dialect binds positionally: the join before
// the filters, the filters before the ordering, the ordering before the page.
func (s *sqlStore) buildSearch(p SearchParams, mode *matchMode, count bool) (*builder, bool) {
	b := newBuilder(s.d)
	if count {
		b.write("SELECT COUNT(*)")
	} else {
		b.write("SELECT " + subtitleColumns)
	}
	b.write(" FROM files f JOIN uploads u ON u.id = f.upload_id")

	var score string
	if mode != nil {
		join, expr, ok := s.d.titleJoin(b, *mode, p.Query)
		if !ok {
			return nil, false
		}
		b.write(" " + join)
		score = expr
	}

	var conds []string
	if p.ImdbID != "" {
		conds = append(conds, "u.imdb_id = "+b.bind(p.ImdbID))
	}
	if p.Language != "" {
		conds = append(conds, "u.language = "+b.bind(p.Language))
	}
	if p.Slug != "" {
		conds = append(conds, "u.slug = "+b.bind(p.Slug))
	}
	if p.HI != nil {
		conds = append(conds, "u.hi = "+b.bind(s.d.boolArg(*p.HI)))
	}
	if p.Year > 0 {
		// A catalogue row without a year must not be filtered away: the year is
		// missing, not different.
		conds = append(conds, "(u.year = "+b.bind(p.Year)+" OR u.year = 0)")
	}
	if p.SeasonEp != "" {
		pattern := "%" + p.SeasonEp + "%"
		like := s.d.likeOp()
		conds = append(conds, "(u.releases "+like+" "+b.bind(pattern)+" OR f.filename "+like+" "+b.bind(pattern)+")")
	}
	if len(conds) > 0 {
		b.write(" WHERE " + strings.Join(conds, " AND "))
	}

	if count {
		return b, true
	}

	order := "f.downloads DESC, f.id ASC"
	if mode != nil {
		// Exact title first, then the closest titles: Bazarr only reads the
		// first page, so the ranking decides whether it sees the right subtitle
		// at all. The engine score is a tie-break, never the primary key.
		exact := b.bind(title.Slugify(p.Query))
		order = "CASE WHEN u.slug = " + exact + " THEN 0 ELSE 1 END ASC, " +
			s.d.lengthFn() + "(u.title) ASC, " + score + " DESC, " + order
	}
	b.write(" ORDER BY " + order)
	b.write(" LIMIT " + b.bind(p.Limit) + " OFFSET " + b.bind(p.Offset))
	return b, true
}

func (s *sqlStore) search(ctx context.Context, p SearchParams, mode *matchMode) ([]Subtitle, int, error) {
	countBuilder, ok := s.buildSearch(p, mode, true)
	if !ok {
		return nil, 0, nil
	}

	var total int
	if err := s.db.QueryRowContext(ctx, countBuilder.String(), countBuilder.args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count: %w", err)
	}
	if total == 0 {
		return nil, 0, nil
	}

	pageBuilder, _ := s.buildSearch(p, mode, false)
	rows, err := s.db.QueryContext(ctx, pageBuilder.String(), pageBuilder.args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []Subtitle
	for rows.Next() {
		sub, err := scanSubtitle(rows.Scan)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, sub)
	}
	return out, total, rows.Err()
}

// ─── importing ───────────────────────────────────────────────────────────────

// uploadSelect is the projection scanUpload reads.
const uploadSelect = "id, subscene_id, file_path, slug, title, imdb_id, language, hi, year, author, author_id, comment, releases, uploaded_at"

func scanUpload(scan func(...any) error) (Upload, error) {
	var u Upload
	err := scan(&u.ID, &u.SubsceneID, &u.FilePath, &u.Slug, &u.Title, &u.ImdbID,
		&u.Language, &u.HI, &u.Year, &u.Author, &u.AuthorID, &u.Comment, &u.Releases, &u.UploadedAt)
	return u, err
}

var uploadColumns = []string{
	"id", "subscene_id", "file_path", "slug", "title", "imdb_id", "language",
	"hi", "year", "author", "author_id", "comment", "releases", "uploaded_at",
}

// UpsertUploads writes catalogue rows. Re-running an import must not duplicate
// or lose anything, so a known upload is updated in place and keeps its id — and
// with it, its files.
func (s *sqlStore) UpsertUploads(ctx context.Context, uploads []Upload) error {
	if len(uploads) == 0 {
		return nil
	}
	update := uploadColumns[1:] // everything except the primary key
	stmt := "INSERT INTO uploads (" + strings.Join(uploadColumns, ", ") + ") VALUES (" +
		s.placeholders(len(uploadColumns)) + ")" + s.d.upsertSuffix([]string{"id"}, update)

	return s.inTx(ctx, func(tx *sql.Tx) error {
		prepared, err := tx.PrepareContext(ctx, stmt)
		if err != nil {
			return err
		}
		defer prepared.Close()

		for _, u := range uploads {
			releases := u.Releases
			if releases == "" {
				releases = "[]"
			}
			_, err := prepared.ExecContext(ctx,
				u.ID, u.SubsceneID, u.FilePath, u.Slug, u.Title, u.ImdbID, u.Language,
				s.d.boolArg(u.HI), u.Year, u.Author, u.AuthorID, u.Comment, releases, u.UploadedAt)
			if err != nil {
				return fmt.Errorf("upsert upload %s: %w", u.ID, err)
			}
		}
		return nil
	})
}

var fileColumns = []string{
	"id", "upload_id", "filename", "format", "content_hash", "content_key", "size",
}

// IngestFiles writes subtitle file rows. The unique key is (upload, content), so
// re-importing the same file updates the existing row and preserves the id an
// operator's Bazarr history already points at.
func (s *sqlStore) IngestFiles(ctx context.Context, files []IngestFile) error {
	if len(files) == 0 {
		return nil
	}
	update := []string{"filename", "format", "content_key", "size"}
	stmt := "INSERT INTO files (" + strings.Join(fileColumns, ", ") + ") VALUES (" +
		s.placeholders(len(fileColumns)) + ")" + s.d.upsertSuffix([]string{"upload_id", "content_hash"}, update)

	return s.inTx(ctx, func(tx *sql.Tx) error {
		prepared, err := tx.PrepareContext(ctx, stmt)
		if err != nil {
			return err
		}
		defer prepared.Close()

		for _, f := range files {
			_, err := prepared.ExecContext(ctx,
				f.ID, f.UploadID, f.Filename, f.Format, f.ContentHash, f.ContentKey, f.Size)
			if err != nil {
				return fmt.Errorf("ingest file %s: %w", f.ID, err)
			}
		}
		return nil
	})
}

// UploadsByPath resolves archive entry paths to catalogue rows in one query, so
// that streaming the archive costs one round trip per batch rather than per file.
func (s *sqlStore) UploadsByPath(ctx context.Context, paths []string) (map[string]Upload, error) {
	out := make(map[string]Upload, len(paths))
	if len(paths) == 0 {
		return out, nil
	}

	b := newBuilder(s.d)
	b.write("SELECT " + uploadSelect + " FROM uploads WHERE file_path IN (")
	for i, p := range paths {
		if i > 0 {
			b.write(", ")
		}
		b.write(b.bind(p))
	}
	b.write(")")

	rows, err := s.db.QueryContext(ctx, b.String(), b.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		u, err := scanUpload(rows.Scan)
		if err != nil {
			return nil, err
		}
		out[u.FilePath] = u
	}
	return out, rows.Err()
}

// UploadsByID resolves catalogue rows by their Subscene id, which is what an
// archive file name carries. It is the fallback for an archive whose paths do
// not match the catalogue's — a re-packed dump, or one with a different prefix.
func (s *sqlStore) UploadsByID(ctx context.Context, ids []string) (map[string]Upload, error) {
	out := make(map[string]Upload, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	b := newBuilder(s.d)
	b.write("SELECT " + uploadSelect + " FROM uploads WHERE id IN (")
	for i, id := range ids {
		if i > 0 {
			b.write(", ")
		}
		b.write(b.bind(id))
	}
	b.write(")")

	rows, err := s.db.QueryContext(ctx, b.String(), b.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		u, err := scanUpload(rows.Scan)
		if err != nil {
			return nil, err
		}
		out[u.ID] = u
	}
	return out, rows.Err()
}

// FilesByUpload returns the files already stored for a batch of uploads, so the
// importer can tell a new file from one it has seen before without a query per
// file.
func (s *sqlStore) FilesByUpload(ctx context.Context, uploadIDs []string) (map[string][]File, error) {
	out := make(map[string][]File, len(uploadIDs))
	if len(uploadIDs) == 0 {
		return out, nil
	}

	b := newBuilder(s.d)
	b.write("SELECT id, upload_id, filename, format, content_hash, content_key, size, downloads FROM files WHERE upload_id IN (")
	for i, id := range uploadIDs {
		if i > 0 {
			b.write(", ")
		}
		b.write(b.bind(id))
	}
	b.write(")")

	rows, err := s.db.QueryContext(ctx, b.String(), b.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var f File
		if err := rows.Scan(&f.ID, &f.UploadID, &f.Filename, &f.Format, &f.ContentHash,
			&f.ContentKey, &f.Size, &f.Downloads); err != nil {
			return nil, err
		}
		out[f.UploadID] = append(out[f.UploadID], f)
	}
	return out, rows.Err()
}

// ─── maintenance ─────────────────────────────────────────────────────────────

// PruneScope reports how much a prune would remove, without removing anything.
// Bytes is the size the `files` rows account for; storage reclaims slightly less
// when several rows share one content-addressed object.
func (s *sqlStore) PruneScope(ctx context.Context, keep []string) (rows int64, bytes int64, err error) {
	b := newBuilder(s.d)
	b.write("SELECT COUNT(*), COALESCE(SUM(f.size), 0) FROM files f JOIN uploads u ON u.id = f.upload_id")
	s.writeNotKept(b, keep)
	err = s.db.QueryRowContext(ctx, b.String(), b.args...).Scan(&rows, &bytes)
	return rows, bytes, err
}

// PruneLanguages deletes one batch of files whose upload language is outside the
// whitelist and reports the storage keys nothing references any more. Content is
// addressed by hash and therefore shared between uploads, so a key is only
// orphaned once every row pointing at it is gone — which is why the caller
// deletes storage objects from this list and not from the rows it just removed.
//
// A batch with Deleted == 0 means there is nothing left to prune.
func (s *sqlStore) PruneLanguages(ctx context.Context, keep []string, batch int) (PruneBatch, error) {
	var result PruneBatch

	b := newBuilder(s.d)
	b.write("SELECT f.id, f.content_key, f.size FROM files f JOIN uploads u ON u.id = f.upload_id")
	s.writeNotKept(b, keep)
	b.write(" LIMIT " + b.bind(batch))

	rows, err := s.db.QueryContext(ctx, b.String(), b.args...)
	if err != nil {
		return result, err
	}
	var ids, keys []string
	for rows.Next() {
		var (
			id, key string
			size    int64
		)
		if err := rows.Scan(&id, &key, &size); err != nil {
			rows.Close()
			return result, err
		}
		ids = append(ids, id)
		if key != "" {
			keys = append(keys, key)
		}
		result.Bytes += size
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return result, err
	}
	if len(ids) == 0 {
		return result, nil
	}

	if err := s.inTx(ctx, func(tx *sql.Tx) error {
		del := newBuilder(s.d)
		del.write("DELETE FROM files WHERE id IN (")
		for i, id := range ids {
			if i > 0 {
				del.write(", ")
			}
			del.write(del.bind(id))
		}
		del.write(")")
		_, err := tx.ExecContext(ctx, del.String(), del.args...)
		return err
	}); err != nil {
		return result, err
	}
	result.Deleted = len(ids)

	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		q := newBuilder(s.d)
		q.write("SELECT 1 FROM files WHERE content_key = " + q.bind(key) + " LIMIT 1")
		var one int
		switch err := s.db.QueryRowContext(ctx, q.String(), q.args...).Scan(&one); err {
		case sql.ErrNoRows:
			result.OrphanKeys = append(result.OrphanKeys, key)
		case nil:
		default:
			return result, err
		}
	}
	return result, nil
}

// writeNotKept restricts a prune to the languages outside the whitelist. An empty
// whitelist would mean "keep nothing", which is never what an operator means, so
// it is rejected by the command rather than silently deleting everything.
func (s *sqlStore) writeNotKept(b *builder, keep []string) {
	if len(keep) == 0 {
		b.write(" WHERE 1 = 0")
		return
	}
	b.write(" WHERE u.language NOT IN (")
	for i, l := range keep {
		if i > 0 {
			b.write(", ")
		}
		b.write(b.bind(l))
	}
	b.write(")")
}

// Reindex rebuilds the title index from the catalogue. It is derived data, so
// this is safe to run at any time; the importer runs it once at the end rather
// than maintaining the index row by row.
func (s *sqlStore) Reindex(ctx context.Context) error {
	return s.d.reindexTitles(ctx, s.db)
}

// Optimize brings derived data up to date without doing the work when it is
// already current: the title index is rebuilt only if it is empty while the
// catalogue is not, and statistics are refreshed so the planner picks the
// composite indexes.
func (s *sqlStore) Optimize(ctx context.Context) error {
	indexed, err := s.d.titleIndexSize(ctx, s.db)
	if err != nil {
		return fmt.Errorf("read title index: %w", err)
	}
	if indexed == 0 {
		uploads, err := s.CountUploads(ctx)
		if err != nil {
			return err
		}
		if uploads > 0 {
			if err := s.Reindex(ctx); err != nil {
				return fmt.Errorf("rebuild title index: %w", err)
			}
		}
	}
	return s.d.analyze(ctx, s.db)
}

// ─── plumbing ────────────────────────────────────────────────────────────────

func (s *sqlStore) placeholders(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = s.d.placeholder(i + 1)
	}
	return strings.Join(parts, ", ")
}

func (s *sqlStore) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
