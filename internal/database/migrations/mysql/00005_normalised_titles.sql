-- Search the title the way a query is read. See the SQLite migration of the same
-- version for why.
--
-- The table is dropped and recreated rather than altered: MySQL cannot add a
-- column and a full-text key over it in one statement, and every statement here
-- runs outside a transaction. Nothing is lost — the title index is derived from
-- `uploads` and Store.Optimize rebuilds it at the next start.

-- +goose NO TRANSACTION
-- +goose Up
DROP TABLE IF EXISTS titles;

-- +goose StatementBegin
CREATE TABLE titles (
    slug       VARCHAR(255) PRIMARY KEY,
    title      VARCHAR(512) NOT NULL,
    normalised VARCHAR(512) NOT NULL,
    FULLTEXT KEY ft_titles_normalised (normalised)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
-- +goose StatementEnd

-- +goose Down
SELECT 1;
