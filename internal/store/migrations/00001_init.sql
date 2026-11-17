-- +goose Up
CREATE TABLE project (
  id                INTEGER PRIMARY KEY,
  name              TEXT NOT NULL,
  root_path         TEXT NOT NULL UNIQUE,
  build_tool        TEXT NOT NULL DEFAULT 'maven',
  executable        TEXT,
  java_home         TEXT,
  env_json          TEXT NOT NULL DEFAULT '{}',
  skip_presets_json TEXT NOT NULL DEFAULT '[]',
  created_at        TEXT NOT NULL
);

CREATE TABLE module (
  id              INTEGER PRIMARY KEY,
  project_id      INTEGER NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  group_id        TEXT NOT NULL,
  artifact_id     TEXT NOT NULL,
  rel_path        TEXT NOT NULL,
  packaging       TEXT NOT NULL,
  parent_rel_path TEXT,
  profiles_json   TEXT NOT NULL DEFAULT '[]',
  deps_json       TEXT NOT NULL DEFAULT '[]',
  source          TEXT NOT NULL,
  discovered_at   TEXT NOT NULL,
  UNIQUE (project_id, group_id, artifact_id)
);

CREATE TABLE run_profile (
  id         INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  name       TEXT NOT NULL,
  spec_json  TEXT NOT NULL,
  pinned     INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  UNIQUE (project_id, name)
);

CREATE TABLE job (
  id           INTEGER PRIMARY KEY,
  project_id   INTEGER NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  profile_id   INTEGER REFERENCES run_profile(id) ON DELETE SET NULL,
  spec_json    TEXT NOT NULL,
  argv_json    TEXT NOT NULL,
  status       TEXT NOT NULL,
  exit_code    INTEGER,
  queued_at    TEXT NOT NULL,
  started_at   TEXT,
  finished_at  TEXT,
  git_head     TEXT,
  log_path     TEXT NOT NULL,
  log_lines    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX job_project_queued ON job(project_id, queued_at DESC);

CREATE TABLE job_module (
  job_id      INTEGER NOT NULL REFERENCES job(id) ON DELETE CASCADE,
  module_key  TEXT NOT NULL,
  ord         INTEGER NOT NULL,
  status      TEXT NOT NULL,
  started_at  TEXT,
  duration_ms INTEGER,
  PRIMARY KEY (job_id, module_key)
);

CREATE TABLE job_problem (
  id         INTEGER PRIMARY KEY,
  job_id     INTEGER NOT NULL REFERENCES job(id) ON DELETE CASCADE,
  module_key TEXT,
  kind       TEXT NOT NULL,
  file       TEXT,
  line       INTEGER,
  message    TEXT NOT NULL,
  log_line   INTEGER
);

CREATE TABLE shell_command (
  id         INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  name       TEXT NOT NULL,
  argv_json  TEXT NOT NULL,
  cwd        TEXT NOT NULL DEFAULT '.',
  UNIQUE (project_id, name)
);

-- +goose Down
DROP TABLE shell_command;
DROP TABLE job_problem;
DROP TABLE job_module;
DROP TABLE job;
DROP TABLE run_profile;
DROP TABLE module;
DROP TABLE project;
