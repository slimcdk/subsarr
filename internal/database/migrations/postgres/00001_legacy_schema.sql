-- The flat schema every installation before this release runs. A fresh database
-- creates it empty and 00004 drops it again; an existing database is stamped at
-- this version without executing anything (see database.Migrator baselining), so
-- both converge on the same starting point.

-- +goose Up
CREATE TABLE IF NOT EXISTS subtitles (
    id           TEXT PRIMARY KEY,
    subscene_id  TEXT NOT NULL,
    title        TEXT NOT NULL,
    slug         TEXT NOT NULL DEFAULT '',
    imdb_id      TEXT NOT NULL DEFAULT '',
    language     TEXT NOT NULL,
    hi           BOOLEAN NOT NULL DEFAULT FALSE,
    author       TEXT NOT NULL DEFAULT '',
    releases     JSONB NOT NULL DEFAULT '[]',
    comment      TEXT NOT NULL DEFAULT '',
    year         INTEGER NOT NULL DEFAULT 0,
    filename     TEXT NOT NULL,
    format       TEXT NOT NULL DEFAULT '',
    content_key  TEXT NOT NULL DEFAULT '',
    content_hash TEXT NOT NULL DEFAULT '',
    uploaded_at  TIMESTAMPTZ,
    downloads    INTEGER NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS subtitles;
