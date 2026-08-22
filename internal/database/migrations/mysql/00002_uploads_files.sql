-- The catalogue model: one `uploads` row per Subscene upload (metadata once),
-- one `files` row per downloadable subtitle file.
--
-- Indexes are declared inline: MySQL has no CREATE INDEX IF NOT EXISTS, and this
-- migration must be safe to re-run after a partial failure.

-- +goose NO TRANSACTION
-- +goose Up
CREATE TABLE IF NOT EXISTS uploads (
    id          VARCHAR(64) PRIMARY KEY,
    subscene_id VARCHAR(64) NOT NULL DEFAULT '',
    file_path   VARCHAR(512) NOT NULL DEFAULT '',
    slug        VARCHAR(255) NOT NULL DEFAULT '',
    title       VARCHAR(512) NOT NULL DEFAULT '',
    imdb_id     VARCHAR(20) NOT NULL DEFAULT '',
    language    VARCHAR(100) NOT NULL DEFAULT '',
    hi          BOOLEAN NOT NULL DEFAULT FALSE,
    year        INT NOT NULL DEFAULT 0,
    author      VARCHAR(255) NOT NULL DEFAULT '',
    author_id   VARCHAR(64) NOT NULL DEFAULT '',
    comment     TEXT NOT NULL,
    releases    TEXT NOT NULL,
    uploaded_at VARCHAR(32) NOT NULL DEFAULT '',
    INDEX idx_uploads_imdb_lang (imdb_id, language),
    INDEX idx_uploads_slug_lang (slug, language),
    INDEX idx_uploads_language (language),
    INDEX idx_uploads_file_path (file_path(255))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS files (
    id           VARCHAR(36) PRIMARY KEY,
    upload_id    VARCHAR(64) NOT NULL,
    filename     VARCHAR(512) NOT NULL DEFAULT '',
    format       VARCHAR(16) NOT NULL DEFAULT '',
    content_hash VARCHAR(64) NOT NULL DEFAULT '',
    content_key  VARCHAR(512) NOT NULL DEFAULT '',
    size         BIGINT NOT NULL DEFAULT 0,
    downloads    INT NOT NULL DEFAULT 0,
    UNIQUE INDEX idx_files_upload_content (upload_id, content_hash),
    INDEX idx_files_upload (upload_id),
    INDEX idx_files_content_key (content_key(255))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS titles (
    slug  VARCHAR(255) PRIMARY KEY,
    title VARCHAR(512) NOT NULL,
    FULLTEXT KEY ft_titles_title (title)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS titles;
DROP TABLE IF EXISTS files;
DROP TABLE IF EXISTS uploads;
