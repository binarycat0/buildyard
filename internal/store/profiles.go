package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// RunProfile is a saved, named RunSpec of a project. The UI calls it a task.
type RunProfile struct {
	ID        int64           `json:"id"`
	ProjectID int64           `json:"projectId"`
	Name      string          `json:"name"`
	Spec      json.RawMessage `json:"spec"`
	Pinned    bool            `json:"pinned"`
	UpdatedAt time.Time       `json:"updatedAt"`
	LastJob   *JobSummary     `json:"lastJob"`
}

const profileCols = `r.id, r.project_id, r.name, r.spec_json, r.pinned, r.updated_at,
	j.id, j.status, j.queued_at, j.started_at, j.finished_at`

const profileFrom = ` FROM run_profile r LEFT JOIN job j ON j.id = (
	SELECT id FROM job WHERE profile_id = r.id ORDER BY queued_at DESC, id DESC LIMIT 1)`

// CreateProfile saves a new profile; names are unique per project.
func (s *Store) CreateProfile(ctx context.Context, p RunProfile) (RunProfile, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO run_profile (project_id, name, spec_json, pinned, updated_at)
		VALUES (?, ?, ?, ?, ?)`, p.ProjectID, p.Name, string(p.Spec), p.Pinned, nowNano())
	if err != nil {
		return RunProfile{}, mapErr(err)
	}
	id, _ := res.LastInsertId()
	return s.GetProfile(ctx, id)
}

// UpdateProfile replaces name, spec and pinned.
func (s *Store) UpdateProfile(ctx context.Context, p RunProfile) (RunProfile, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE run_profile SET name = ?, spec_json = ?, pinned = ?, updated_at = ? WHERE id = ?`,
		p.Name, string(p.Spec), p.Pinned, nowNano(), p.ID)
	if err != nil {
		return RunProfile{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return RunProfile{}, ErrNotFound
	}
	return s.GetProfile(ctx, p.ID)
}

// DeleteProfile removes a profile; its jobs keep their spec snapshot.
func (s *Store) DeleteProfile(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM run_profile WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetProfile returns one profile with its last job.
func (s *Store) GetProfile(ctx context.Context, id int64) (RunProfile, error) {
	p, err := scanProfile(s.db.QueryRowContext(ctx, `SELECT `+profileCols+profileFrom+` WHERE r.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return RunProfile{}, ErrNotFound
	}
	return p, err
}

// ListProfiles returns a project's profiles: pinned first, then by name.
func (s *Store) ListProfiles(ctx context.Context, projectID int64) ([]RunProfile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+profileCols+profileFrom+` WHERE r.project_id = ?
		ORDER BY r.pinned DESC, r.name COLLATE NOCASE`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RunProfile{}
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func scanProfile(r scanner) (RunProfile, error) {
	var (
		p                                  RunProfile
		spec, updated                      string
		jobID                              sql.NullInt64
		jobStatus, queued, started, finish sql.NullString
	)
	if err := r.Scan(&p.ID, &p.ProjectID, &p.Name, &spec, &p.Pinned, &updated,
		&jobID, &jobStatus, &queued, &started, &finish); err != nil {
		return RunProfile{}, err
	}
	p.Spec = json.RawMessage(spec)
	p.UpdatedAt = parseTime(updated)
	if jobID.Valid {
		p.LastJob = &JobSummary{ID: jobID.Int64, Status: jobStatus.String, QueuedAt: parseTime(queued.String),
			StartedAt: parseNullTime(started), FinishedAt: parseNullTime(finish)}
	}
	return p, nil
}
