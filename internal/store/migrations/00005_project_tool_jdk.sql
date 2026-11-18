-- +goose Up
-- JDK for the project's build tool (like IntelliJ's "JRE for the Maven importer/runner").
-- NULL = use the project JDK (jdk_id), then the tool's global default.
ALTER TABLE project ADD COLUMN tool_jdk_id INTEGER REFERENCES jdk(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE project DROP COLUMN tool_jdk_id;
