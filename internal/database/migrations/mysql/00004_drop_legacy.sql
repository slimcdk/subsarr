-- Runs after the version 3 data migration has copied every legacy row across.

-- +goose NO TRANSACTION
-- +goose Up
DROP TABLE IF EXISTS subtitles;

-- +goose Down
SELECT 1;
