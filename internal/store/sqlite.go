package store

import (
	"context"
	"database/sql"
	"strings"

	"github.com/slimcdk/subsarr/internal/database/sqlitedb"
)

type sqliteStore struct {
	q  *sqlitedb.Queries
	db *sql.DB
}

func (s *sqliteStore) GetSubtitle(ctx context.Context, id string) (*Subtitle, error) {
	row, err := s.q.GetSubtitle(ctx, id)
	if err != nil {
		return nil, err
	}
	return &Subtitle{
		ID:         row.ID,
		SubsceneID: row.SubsceneID,
		Title:      row.Title,
		Slug:       row.Slug,
		ImdbID:     row.ImdbID,
		Language:   row.Language,
		HI:         row.Hi != 0,
		Author:     row.Author,
		Releases:   row.Releases,
		Comment:    row.Comment,
		Year:       int(row.Year),
		Filename:   row.Filename,
		Format:     row.Format,
		ContentKey: row.ContentKey,
		UploadedAt: row.UploadedAt,
		Downloads:  int(row.Downloads),
	}, nil
}

func (s *sqliteStore) InsertSubtitle(ctx context.Context, sub *Subtitle) (bool, error) {
	var hi int64
	if sub.HI {
		hi = 1
	}
	result, err := s.q.InsertSubtitle(ctx, sqlitedb.InsertSubtitleParams{
		ID:         sub.ID,
		SubsceneID: sub.SubsceneID,
		Title:      sub.Title,
		Slug:       sub.Slug,
		ImdbID:     sub.ImdbID,
		Language:   sub.Language,
		Hi:         hi,
		Author:     sub.Author,
		Releases:   sub.Releases,
		Comment:    sub.Comment,
		Year:       int64(sub.Year),
		Filename:   sub.Filename,
		Format:     sub.Format,
		ContentKey: sub.ContentKey,
		UploadedAt: sub.UploadedAt,
		Downloads:  int64(sub.Downloads),
	})
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	return n > 0, nil
}

func (s *sqliteStore) InsertSubtitleBatch(ctx context.Context, subs []*Subtitle) (inserted, skipped, errors int) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, len(subs)
	}
	qtx := s.q.WithTx(tx)
	for _, sub := range subs {
		var hi int64
		if sub.HI {
			hi = 1
		}
		result, err := qtx.InsertSubtitle(ctx, sqlitedb.InsertSubtitleParams{
			ID:         sub.ID,
			SubsceneID: sub.SubsceneID,
			Title:      sub.Title,
			Slug:       sub.Slug,
			ImdbID:     sub.ImdbID,
			Language:   sub.Language,
			Hi:         hi,
			Author:     sub.Author,
			Releases:   sub.Releases,
			Comment:    sub.Comment,
			Year:       int64(sub.Year),
			Filename:   sub.Filename,
			Format:     sub.Format,
			ContentKey: sub.ContentKey,
			UploadedAt: sub.UploadedAt,
			Downloads:  int64(sub.Downloads),
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

func (s *sqliteStore) IncrementDownloads(ctx context.Context, id string) error {
	return s.q.IncrementDownloads(ctx, id)
}

func (s *sqliteStore) ListLanguages(ctx context.Context) ([]LanguageCount, error) {
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

func (s *sqliteStore) SearchSubtitles(ctx context.Context, p SearchParams) ([]Subtitle, error) {
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
		where = append(where, "hi = 1")
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
       comment, year, filename, format, content_key, uploaded_at, downloads FROM subtitles`
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
		var hi int64
		if err := rows.Scan(
			&sub.ID, &sub.SubsceneID, &sub.Title, &sub.Slug, &sub.ImdbID,
			&sub.Language, &hi, &sub.Author, &sub.Releases, &sub.Comment,
			&sub.Year, &sub.Filename, &sub.Format, &sub.ContentKey,
			&sub.UploadedAt, &sub.Downloads,
		); err != nil {
			return nil, err
		}
		sub.HI = hi != 0
		results = append(results, sub)
	}
	return results, rows.Err()
}
