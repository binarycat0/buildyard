-- +goose Up
CREATE TABLE jdk (
  id          INTEGER PRIMARY KEY,
  home        TEXT NOT NULL UNIQUE,         -- canonical JDK home
  name        TEXT NOT NULL DEFAULT '',     -- user label; empty = generated from version/vendor
  version     TEXT NOT NULL DEFAULT '',
  major       INTEGER NOT NULL DEFAULT 0,
  vendor      TEXT NOT NULL DEFAULT '',
  arch        TEXT NOT NULL DEFAULT '',
  source      TEXT NOT NULL,                -- manual | macos | homebrew | sdkman | jenv | ...
  missing     INTEGER NOT NULL DEFAULT 0,   -- 1 when the last detection did not find it
  detected_at TEXT NOT NULL
);

CREATE TABLE setting (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL                       -- JSON
);

-- Projects now pick a JDK from the list instead of a free-form JAVA_HOME,
-- and executable holds a mode: auto | wrapper | mvnd | mvn | /abs/path (NULL = global default).
ALTER TABLE project DROP COLUMN java_home;
ALTER TABLE project ADD COLUMN jdk_id INTEGER REFERENCES jdk(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE project DROP COLUMN jdk_id;
ALTER TABLE project ADD COLUMN java_home TEXT;
DROP TABLE setting;
DROP TABLE jdk;
