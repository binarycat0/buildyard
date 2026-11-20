-- +goose Up
-- Task defaults: profiles, -D properties and skips every task of the project gets.
ALTER TABLE project ADD COLUMN task_defaults_json TEXT NOT NULL DEFAULT '{}';
-- A project now has one JDK for its build. The build tool's own JDK, when set, was the
-- one runs used, so it becomes the project JDK.
UPDATE project SET jdk_id = tool_jdk_id WHERE tool_jdk_id IS NOT NULL;
UPDATE project SET tool_jdk_id = NULL;

-- +goose Down
ALTER TABLE project DROP COLUMN task_defaults_json;
