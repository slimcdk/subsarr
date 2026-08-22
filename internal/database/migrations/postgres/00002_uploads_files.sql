-- The catalogue model: one `uploads` row per Subscene upload (metadata once),
-- one `files` row per downloadable subtitle file.

-- +goose Up
CREATE TABLE IF NOT EXISTS uploads (
    id          TEXT PRIMARY KEY,
    subscene_id TEXT NOT NULL DEFAULT '',
    file_path   TEXT NOT NULL DEFAULT '',
    slug        TEXT NOT NULL DEFAULT '',
    title       TEXT NOT NULL DEFAULT '',
    imdb_id     TEXT NOT NULL DEFAULT '',
    language    TEXT NOT NULL DEFAULT '',
    hi          BOOLEAN NOT NULL DEFAULT FALSE,
    year        INTEGER NOT NULL DEFAULT 0,
    author      TEXT NOT NULL DEFAULT '',
    author_id   TEXT NOT NULL DEFAULT '',
    comment     TEXT NOT NULL DEFAULT '',
    releases    TEXT NOT NULL DEFAULT '[]',
    uploaded_at TEXT NOT NULL DEFAULT ''
);

-- Bazarr's first call is always (imdb_id, language); the title path funnels into
-- (slug, language) once the query has been resolved to slugs.
CREATE INDEX IF NOT EXISTS idx_uploads_imdb_lang ON uploads(imdb_id, language);
CREATE INDEX IF NOT EXISTS idx_uploads_slug_lang ON uploads(slug, language);
CREATE INDEX IF NOT EXISTS idx_uploads_language ON uploads(language);
CREATE INDEX IF NOT EXISTS idx_uploads_file_path ON uploads(file_path);

CREATE TABLE IF NOT EXISTS files (
    id           TEXT PRIMARY KEY,
    upload_id    TEXT NOT NULL,
    filename     TEXT NOT NULL DEFAULT '',
    format       TEXT NOT NULL DEFAULT '',
    content_hash TEXT NOT NULL DEFAULT '',
    content_key  TEXT NOT NULL DEFAULT '',
    size         BIGINT NOT NULL DEFAULT 0,
    downloads    INTEGER NOT NULL DEFAULT 0
);

-- The dedup key: the same subtitle re-extracted from the same upload keeps its id.
CREATE UNIQUE INDEX IF NOT EXISTS idx_files_upload_content ON files(upload_id, content_hash);
CREATE INDEX IF NOT EXISTS idx_files_upload ON files(upload_id);
CREATE INDEX IF NOT EXISTS idx_files_content_key ON files(content_key);

-- Title index over the ~150k distinct slugs rather than the millions of uploads.
CREATE TABLE IF NOT EXISTS titles (
    slug  TEXT PRIMARY KEY,
    title TEXT NOT NULL DEFAULT '',
    tsv   tsvector GENERATED ALWAYS AS (to_tsvector('simple', title)) STORED
);
CREATE INDEX IF NOT EXISTS idx_titles_tsv ON titles USING GIN (tsv);

-- +goose StatementBegin
-- pg_trgm needs privileges a managed PostgreSQL user may not have. Without it the
-- substring fallback degrades to LIKE, which is correct but slower, so a failure
-- here must not fail the migration.
DO $$
BEGIN
    CREATE EXTENSION IF NOT EXISTS pg_trgm;
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'pg_trgm not available: substring title search falls back to LIKE';
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm') THEN
        CREATE INDEX IF NOT EXISTS idx_titles_trgm ON titles USING GIN (title gin_trgm_ops);
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS titles;
DROP TABLE IF EXISTS files;
DROP TABLE IF EXISTS uploads;
