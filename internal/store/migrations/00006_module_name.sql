-- +goose Up
-- Discovery through Maven gives the reactor order and the <name> of each module.
ALTER TABLE module ADD COLUMN name TEXT NOT NULL DEFAULT '';
ALTER TABLE module ADD COLUMN ord INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE module DROP COLUMN ord;
ALTER TABLE module DROP COLUMN name;
