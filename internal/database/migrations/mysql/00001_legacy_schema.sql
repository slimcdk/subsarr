-- The flat schema every installation before this release runs. A fresh database
-- creates it empty and 00004 drops it again; an existing database is stamped at
-- this version without executing anything (see database.Migrator baselining), so
-- both converge on the same starting point.
--
-- MySQL commits DDL implicitly, so every migration in this dialect runs outside a
-- transaction and each statement must be idempotent on its own.

-- +goose NO TRANSACTION
-- +goose Up
CREATE TABLE IF NOT EXISTS subtitles (
    id           VARCHAR(36) PRIMARY KEY,
    subscene_id  VARCHAR(255) NOT NULL,
    title        TEXT NOT NULL,
    slug         VARCHAR(255) NOT NULL DEFAULT '',
    imdb_id      VARCHAR(20) NOT NULL DEFAULT '',
    language     VARCHAR(100) NOT NULL,
    hi           BOOLEAN NOT NULL DEFAULT FALSE,
    author       VARCHAR(255) NOT NULL DEFAULT '',
    releases     JSON NOT NULL,
    comment      TEXT NOT NULL,
    year         INT NOT NULL DEFAULT 0,
    filename     VARCHAR(512) NOT NULL,
    format       VARCHAR(10) NOT NULL DEFAULT '',
    content_key  VARCHAR(512) NOT NULL DEFAULT '',
    content_hash VARCHAR(64) NOT NULL DEFAULT '',
    uploaded_at  DATETIME NULL,
    downloads    INT NOT NULL DEFAULT 0,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_subtitles_content_dedup (subscene_id, content_hash),
    INDEX idx_subtitles_imdb_id (imdb_id),
    INDEX idx_subtitles_language (language),
    INDEX idx_subtitles_slug (slug)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down
DROP TABLE IF EXISTS subtitles;
