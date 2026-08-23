-- Search the title the way a query is read.
--
-- A query is normalised before it is matched — apostrophes dropped, dotted
-- acronyms collapsed, punctuation reduced to word boundaries — while the index
-- held the title verbatim. The two could never meet: "Don't Look Up" indexes as
-- [don, t, look, up] and asks for [dont, look, up].
--
-- `normalised` holds the same reduction the query goes through, and is what the
-- index is built over. `title` stays as it is: it is what a client displays and
-- what results are ordered by.

-- +goose Up
ALTER TABLE titles ADD COLUMN normalised TEXT NOT NULL DEFAULT '';

DROP TABLE IF EXISTS titles_fts;
DROP TABLE IF EXISTS titles_trgm;
CREATE VIRTUAL TABLE titles_fts USING fts5(slug UNINDEXED, normalised, tokenize='unicode61');
CREATE VIRTUAL TABLE titles_trgm USING fts5(slug UNINDEXED, normalised, tokenize='trigram');

-- The index is derived data: Store.Reindex fills it, and Store.Optimize notices
-- at the next start that it is empty and rebuilds it.
DELETE FROM titles;

-- +goose Down
DROP TABLE IF EXISTS titles_trgm;
DROP TABLE IF EXISTS titles_fts;
CREATE VIRTUAL TABLE titles_fts USING fts5(slug UNINDEXED, title, tokenize='unicode61');
CREATE VIRTUAL TABLE titles_trgm USING fts5(slug UNINDEXED, title, tokenize='trigram');
ALTER TABLE titles DROP COLUMN normalised;
