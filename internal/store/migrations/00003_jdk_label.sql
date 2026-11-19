-- +goose Up
ALTER TABLE jdk ADD COLUMN label TEXT NOT NULL DEFAULT '';    -- derived from the install location
ALTER TABLE jdk ADD COLUMN found_at TEXT NOT NULL DEFAULT ''; -- path it was found at, if a symlink

-- +goose Down
ALTER TABLE jdk DROP COLUMN found_at;
ALTER TABLE jdk DROP COLUMN label;
