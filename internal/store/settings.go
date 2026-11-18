package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Settings are the global, UI-editable settings.
type Settings struct {
	DefaultExecutable string `json:"defaultExecutable"` // auto | wrapper | mvnd | mvn | /abs/path
	DefaultJdkID      *int64 `json:"defaultJdkId"`      // nil = whatever JAVA_HOME the daemon has
}

// DefaultSettings are used for keys that were never saved.
func DefaultSettings() Settings { return Settings{DefaultExecutable: "auto"} }

// GetSettings reads all settings.
func (s *Store) GetSettings(ctx context.Context) (Settings, error) {
	out := DefaultSettings()
	for key, dst := range map[string]any{"defaultExecutable": &out.DefaultExecutable, "defaultJdkId": &out.DefaultJdkID} {
		var raw string
		err := s.db.QueryRowContext(ctx, `SELECT value FROM setting WHERE key = ?`, key).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return out, err
		}
		if err := json.Unmarshal([]byte(raw), dst); err != nil {
			return out, err
		}
	}
	// A deleted JDK leaves a dangling default; treat it as unset.
	if out.DefaultJdkID != nil {
		if _, err := s.GetJDK(ctx, *out.DefaultJdkID); errors.Is(err, ErrNotFound) {
			out.DefaultJdkID = nil
		}
	}
	return out, nil
}

// SaveSettings writes all settings.
func (s *Store) SaveSettings(ctx context.Context, st Settings) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key, v := range map[string]any{"defaultExecutable": st.DefaultExecutable, "defaultJdkId": st.DefaultJdkID} {
		b, _ := json.Marshal(v)
		if _, err := tx.ExecContext(ctx, `INSERT INTO setting (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, string(b)); err != nil {
			return err
		}
	}
	return tx.Commit()
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
