-- Search the title the way a query is read. See the SQLite migration of the same
-- version for why.

-- +goose Up
ALTER TABLE titles ADD COLUMN IF NOT EXISTS normalised TEXT NOT NULL DEFAULT '';

DROP INDEX IF EXISTS idx_titles_tsv;
ALTER TABLE titles DROP COLUMN IF EXISTS tsv;
ALTER TABLE titles ADD COLUMN tsv tsvector GENERATED ALWAYS AS (to_tsvector('simple', normalised)) STORED;
CREATE INDEX IF NOT EXISTS idx_titles_tsv ON titles USING GIN (tsv);

DROP INDEX IF EXISTS idx_titles_trgm;

-- +goose StatementBegin
-- As in version 2: without the operator class the substring fallback uses LIKE,
-- which is correct and slower, and is not worth failing an upgrade over.
DO $$
BEGIN
    CREATE INDEX IF NOT EXISTS idx_titles_trgm ON titles USING GIN (normalised gin_trgm_ops);
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'pg_trgm index not created: substring title search falls back to LIKE';
END
$$;
-- +goose StatementEnd

-- The index is derived data: Store.Reindex fills it, and Store.Optimize notices
-- at the next start that it is empty and rebuilds it.
DELETE FROM titles;

-- +goose Down
SELECT 1;
