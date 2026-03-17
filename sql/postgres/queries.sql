-- name: GetSubtitle :one
SELECT id, subscene_id, title, slug, imdb_id, language, hi, author, releases,
       comment, year, filename, format, content_key, uploaded_at, downloads
FROM subtitles WHERE id = $1;

-- name: InsertSubtitle :execresult
INSERT INTO subtitles (
    id, subscene_id, title, slug, imdb_id, language, hi,
    author, releases, comment, year, filename, format,
    content_key, uploaded_at, downloads
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
ON CONFLICT (slug, subscene_id, filename) DO NOTHING;

-- name: ListLanguages :many
SELECT language, COUNT(*) AS count
FROM subtitles
WHERE language != ''
GROUP BY language
ORDER BY count DESC;

-- name: IncrementDownloads :exec
UPDATE subtitles SET downloads = downloads + 1 WHERE id = $1;
