-- +goose Up
-- The Maven extension reports which project aggregates each module (its <modules> parent).
ALTER TABLE module ADD COLUMN aggregator TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE module DROP COLUMN aggregator;
