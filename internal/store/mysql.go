package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/slimcdk/subsarr/internal/database/mysqldb"
)

type mysqlStore struct {
	q  *mysqldb.Queries
	db *sql.DB
}

func (s *mysqlStore) GetSubtitle(ctx context.Context, id string) (*Subtitle, error) {
	row, err := s.q.GetSubtitle(ctx, id)
	if err != nil {
		return nil, err
	}
	var uploadedAt string
	if row.UploadedAt.Valid {
		uploadedAt = row.UploadedAt.Time.UTC().Format(time.RFC3339)
	}
	return &Subtitle{
		ID:         row.ID,
		SubsceneID: row.SubsceneID,
		Title:      row.Title,
		Slug:       row.Slug,
		ImdbID:     row.ImdbID,
		Language:   row.Language,
		HI:         row.Hi,
		Author:     row.Author,
		Releases:   string(row.Releases),
		Comment:    row.Comment,
		Year:       int(row.Year),
		Filename:   row.Filename,
		Format:     row.Format,
		ContentKey:  row.ContentKey,
		ContentHash: row.ContentHash,
		UploadedAt:  uploadedAt,
		Downloads:  int(row.Downloads),
	}, nil
}

func myReleases(s string) []byte {
	if s == "" {
		return []byte("[]")
	}
	return []byte(s)
}

func (s *mysqlStore) InsertSubtitle(ctx context.Context, sub *Subtitle) (bool, error) {
	var uploadedAt sql.NullTime
	if sub.UploadedAt != "" {
		if t, err := time.Parse(time.RFC3339, sub.UploadedAt); err == nil {
			uploadedAt = sql.NullTime{Time: t, Valid: true}
		}
	}
	result, err := s.q.InsertSubtitle(ctx, mysqldb.InsertSubtitleParams{
		ID:          sub.ID,
		SubsceneID:  sub.SubsceneID,
		Title:       sub.Title,
		Slug:        sub.Slug,
		ImdbID:      sub.ImdbID,
		Language:    sub.Language,
		Hi:          sub.HI,
		Author:      sub.Author,
		Releases:    myReleases(sub.Releases),
		Comment:     sub.Comment,
		Year:        int32(sub.Year),
		Filename:    sub.Filename,
		Format:      sub.Format,
		ContentKey:  sub.ContentKey,
		ContentHash: sub.ContentHash,
		UploadedAt:  uploadedAt,
		Downloads:   int32(sub.Downloads),
	})
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	return n > 0, nil
}

func (s *mysqlStore) InsertSubtitleBatch(ctx context.Context, subs []*Subtitle) (inserted, skipped, errors int) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, len(subs)
	}
	qtx := s.q.WithTx(tx)
	for _, sub := range subs {
		var uploadedAt sql.NullTime
		if sub.UploadedAt != "" {
			if t, err := time.Parse(time.RFC3339, sub.UploadedAt); err == nil {
				uploadedAt = sql.NullTime{Time: t, Valid: true}
			}
		}
		result, err := qtx.InsertSubtitle(ctx, mysqldb.InsertSubtitleParams{
			ID:          sub.ID,
			SubsceneID:  sub.SubsceneID,
			Title:       sub.Title,
			Slug:        sub.Slug,
			ImdbID:      sub.ImdbID,
			Language:    sub.Language,
			Hi:          sub.HI,
			Author:      sub.Author,
			Releases:    myReleases(sub.Releases),
			Comment:     sub.Comment,
			Year:        int32(sub.Year),
			Filename:    sub.Filename,
			Format:      sub.Format,
			ContentKey:  sub.ContentKey,
			ContentHash: sub.ContentHash,
			UploadedAt:  uploadedAt,
			Downloads:   int32(sub.Downloads),
		})
		if err != nil {
			errors++
			continue
		}
		n, _ := result.RowsAffected()
		if n > 0 {
			inserted++
		} else {
			skipped++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, len(subs)
	}
	return inserted, skipped, errors
}

func (s *mysqlStore) IncrementDownloads(ctx context.Context, id string) error {
	return s.q.IncrementDownloads(ctx, id)
}

func (s *mysqlStore) ListLanguages(ctx context.Context) ([]LanguageCount, error) {
	rows, err := s.q.ListLanguages(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]LanguageCount, len(rows))
	for i, r := range rows {
		result[i] = LanguageCount{Language: r.Language, Count: int(r.Count)}
	}
	return result, nil
}

func (s *mysqlStore) SearchSubtitles(ctx context.Context, p SearchParams) ([]Subtitle, error) {
	var where []string
	var args []any

	if p.ImdbID != "" {
		where = append(where, "imdb_id = ?")
		args = append(args, p.ImdbID)
	}
	if p.Language != "" {
		where = append(where, "language = ?")
		args = append(args, p.Language)
	}
	if p.Slug != "" {
		where = append(where, "slug = ?")
		args = append(args, p.Slug)
	}
	if p.HI != nil && *p.HI {
		where = append(where, "hi = TRUE")
	}
	if p.Year > 0 {
		where = append(where, "year = ?")
		args = append(args, p.Year)
	}
	if p.Query != "" {
		pattern := "%" + p.Query + "%"
		where = append(where, "(title LIKE ? OR filename LIKE ?)")
		args = append(args, pattern, pattern)
	}
	if p.SeasonEp != "" {
		pattern := "%" + p.SeasonEp + "%"
		where = append(where, "(releases LIKE ? OR filename LIKE ?)")
		args = append(args, pattern, pattern)
	}

	query := `SELECT id, subscene_id, title, slug, imdb_id, language, hi, author, releases,
       comment, year, filename, format, content_key, content_hash, uploaded_at, downloads FROM subtitles`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY downloads DESC LIMIT ? OFFSET ?"
	args = append(args, p.Limit, p.Offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Subtitle
	for rows.Next() {
		var sub Subtitle
		var releases []byte
		var uploadedAt sql.NullTime
		if err := rows.Scan(
			&sub.ID, &sub.SubsceneID, &sub.Title, &sub.Slug, &sub.ImdbID,
			&sub.Language, &sub.HI, &sub.Author, &releases, &sub.Comment,
			&sub.Year, &sub.Filename, &sub.Format, &sub.ContentKey,
			&sub.ContentHash, &uploadedAt, &sub.Downloads,
		); err != nil {
			return nil, err
		}
		sub.Releases = string(releases)
		if uploadedAt.Valid {
			sub.UploadedAt = uploadedAt.Time.UTC().Format(time.RFC3339)
		}
		results = append(results, sub)
	}
	return results, rows.Err()
}
