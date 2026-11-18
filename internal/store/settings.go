package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// ToolSettings are the global defaults for one build tool; projects using that tool can
// override each field. Like IntelliJ, every Java-based tool has its own JDK setting.
type ToolSettings struct {
	Executable string `json:"executable"` // maven: auto | wrapper | mvnd | mvn | /abs/path
	JdkID      *int64 `json:"jdkId"`      // JDK the tool runs on; nil = the daemon's JAVA_HOME
}

// Settings are the global, UI-editable settings.
type Settings struct {
	Tools map[string]ToolSettings `json:"tools"` // keyed by build tool id ("maven"; later "bazel", "gradle")
}

// KnownTools are the build tools that have settings, with their defaults.
func KnownTools() map[string]ToolSettings {
	return map[string]ToolSettings{"maven": {Executable: "auto"}}
}

// DefaultSettings are used for tools that were never saved.
func DefaultSettings() Settings { return Settings{Tools: KnownTools()} }

// Tool returns the settings for one build tool, falling back to its defaults.
func (st Settings) Tool(id string) ToolSettings {
	if t, ok := st.Tools[id]; ok {
		return t
	}
	return KnownTools()[id]
}

func toolKey(id string) string { return "tool." + id }

// GetSettings reads all settings.
func (s *Store) GetSettings(ctx context.Context) (Settings, error) {
	out := DefaultSettings()
	for id, def := range KnownTools() {
		t := def
		if _, err := s.GetJSON(ctx, toolKey(id), &t); err != nil {
			return out, err
		}
		if t.Executable == "" {
			t.Executable = def.Executable
		}
		// A deleted JDK leaves a dangling default; treat it as unset.
		if t.JdkID != nil {
			if _, err := s.GetJDK(ctx, *t.JdkID); errors.Is(err, ErrNotFound) {
				t.JdkID = nil
			}
		}
		out.Tools[id] = t
	}
	return out, nil
}

// SaveSettings writes the settings of every tool present in st.
func (s *Store) SaveSettings(ctx context.Context, st Settings) error {
	for id, t := range st.Tools {
		if err := s.PutJSON(ctx, toolKey(id), t); err != nil {
			return err
		}
	}
	return nil
}

// GetJSON reads a raw JSON value stored under key (used for cached detection results).
// It returns ok=false when the key was never written.
func (s *Store) GetJSON(ctx context.Context, key string, dst any) (bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM setting WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), dst)
}

// PutJSON stores v as JSON under key.
func (s *Store) PutJSON(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO setting (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, string(b))
	return err
}
