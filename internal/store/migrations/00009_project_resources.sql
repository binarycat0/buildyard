-- +goose Up
ALTER TABLE project ADD COLUMN resources_json TEXT NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE project DROP COLUMN resources_json;
