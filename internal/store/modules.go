package store

import (
	"context"
	"encoding/json"
	"time"
)

// Module is one reactor project of a project, in reactor order.
type Module struct {
	Key          string    `json:"key"` // groupId:artifactId
	GroupID      string    `json:"groupId"`
	ArtifactID   string    `json:"artifactId"`
	Name         string    `json:"name"`
	Packaging    string    `json:"packaging"`
	RelPath      string    `json:"relPath"` // "" until the extension reports directories (phase 4)
	Profiles     []string  `json:"profiles"`
	Ord          int       `json:"ord"`
	DiscoveredAt time.Time `json:"discoveredAt"`
}

// ReplaceModules stores the result of a discovery, replacing the previous one.
func (s *Store) ReplaceModules(ctx context.Context, projectID int64, mods []Module, source string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM module WHERE project_id = ?`, projectID); err != nil {
		return err
	}
	at := now()
	for i, m := range mods {
		prof, _ := json.Marshal(nonNil(m.Profiles))
		if _, err := tx.ExecContext(ctx, `INSERT INTO module
			(project_id, group_id, artifact_id, name, packaging, rel_path, profiles_json, source, ord, discovered_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			projectID, m.GroupID, m.ArtifactID, m.Name, m.Packaging, m.RelPath, string(prof), source, i, at); err != nil {
			return mapErr(err)
		}
	}
	return tx.Commit()
}

// ListModules returns a project's modules in reactor order.
func (s *Store) ListModules(ctx context.Context, projectID int64) ([]Module, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT group_id, artifact_id, name, packaging, rel_path, profiles_json, ord, discovered_at
		FROM module WHERE project_id = ? ORDER BY ord, id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Module{}
	for rows.Next() {
		var m Module
		var prof, at string
		if err := rows.Scan(&m.GroupID, &m.ArtifactID, &m.Name, &m.Packaging, &m.RelPath, &prof, &m.Ord, &at); err != nil {
			return nil, err
		}
		m.Key = m.GroupID + ":" + m.ArtifactID
		_ = json.Unmarshal([]byte(prof), &m.Profiles)
		m.Profiles = nonNil(m.Profiles)
		m.DiscoveredAt = parseTime(at)
		out = append(out, m)
	}
	return out, rows.Err()
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
