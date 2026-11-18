-- +goose Up
-- Global defaults move from flat keys to one JSON object per build tool.
INSERT OR IGNORE INTO setting (key, value)
SELECT 'tool.maven', json_object(
         'executable', COALESCE((SELECT json_extract(value, '$') FROM setting WHERE key = 'defaultExecutable'), 'auto'),
         'jdkId',      (SELECT json_extract(value, '$') FROM setting WHERE key = 'defaultJdkId'))
WHERE EXISTS (SELECT 1 FROM setting WHERE key IN ('defaultExecutable', 'defaultJdkId'));
DELETE FROM setting WHERE key IN ('defaultExecutable', 'defaultJdkId');

-- +goose Down
INSERT OR IGNORE INTO setting (key, value)
SELECT 'defaultExecutable', json_quote(json_extract(value, '$.executable')) FROM setting WHERE key = 'tool.maven';
INSERT OR IGNORE INTO setting (key, value)
SELECT 'defaultJdkId', COALESCE(json_extract(value, '$.jdkId'), 'null') FROM setting WHERE key = 'tool.maven';
DELETE FROM setting WHERE key = 'tool.maven';
