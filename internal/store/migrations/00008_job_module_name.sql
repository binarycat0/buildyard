-- +goose Up
ALTER TABLE job_module ADD COLUMN name TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE job_module DROP COLUMN name;
