-- +goose Up
ALTER TABLE job ADD COLUMN dir TEXT;

-- +goose Down
ALTER TABLE job DROP COLUMN dir;
