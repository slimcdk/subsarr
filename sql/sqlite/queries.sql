-- name: GetSubtitle :one
SELECT id, subscene_id, title, slug, imdb_id, language, hi, author, releases,
       comment, year, filename, format, content_key, uploaded_at, downloads
FROM subtitles WHERE id = ?;

-- name: InsertSubtitle :execresult
INSERT OR IGNORE INTO subtitles (
    id, subscene_id, title, slug, imdb_id, language, hi,
    author, releases, comment, year, filename, format,
    content_key, uploaded_at, downloads
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: IncrementDownloads :exec
UPDATE subtitles SET downloads = downloads + 1 WHERE id = ?;

-- name: ListLanguages :many
SELECT language, COUNT(*) AS count
FROM subtitles
WHERE language != ''
GROUP BY language
ORDER BY count DESC;
