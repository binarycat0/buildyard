-- +goose Up
ALTER TABLE job ADD COLUMN trace_id TEXT;

-- +goose Down
ALTER TABLE job DROP COLUMN trace_id;
