CREATE TABLE IF NOT EXISTS subtitles (
    id          TEXT PRIMARY KEY,
    subscene_id TEXT NOT NULL,
    title       TEXT NOT NULL,
    slug        TEXT NOT NULL DEFAULT '',
    imdb_id     TEXT NOT NULL DEFAULT '',
    language    TEXT NOT NULL,
    hi          INTEGER NOT NULL DEFAULT 0,
    author      TEXT NOT NULL DEFAULT '',
    releases    TEXT NOT NULL DEFAULT '[]',
    comment     TEXT NOT NULL DEFAULT '',
    year        INTEGER NOT NULL DEFAULT 0,
    filename    TEXT NOT NULL,
    format      TEXT NOT NULL DEFAULT '',
    content_key TEXT NOT NULL DEFAULT '',
    uploaded_at TEXT NOT NULL DEFAULT '',
    downloads   INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_subtitles_subscene_file ON subtitles(slug, subscene_id, filename);
CREATE INDEX IF NOT EXISTS idx_subtitles_imdb_id ON subtitles(imdb_id);
CREATE INDEX IF NOT EXISTS idx_subtitles_language ON subtitles(language);
CREATE INDEX IF NOT EXISTS idx_subtitles_slug ON subtitles(slug);
CREATE INDEX IF NOT EXISTS idx_subtitles_title_lang ON subtitles(title, language);
