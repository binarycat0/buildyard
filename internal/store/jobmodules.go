package store

import (
	"context"
	"database/sql"
	"time"
)

// JobModule is one module's outcome in a job, from the Maven extension's events.
type JobModule struct {
	Key        string     `json:"key"`
	Name       string     `json:"name"`
	Ord        int        `json:"ord"`
	Status     string     `json:"status"` // pending | running | succeeded | failed | skipped
	StartedAt  *time.Time `json:"startedAt"`
	DurationMs int64      `json:"ms"`
}

// SaveJobModules replaces a job's module rows.
func (s *Store) SaveJobModules(ctx context.Context, jobID int64, mods []JobModule) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM job_module WHERE job_id = ?`, jobID); err != nil {
		return err
	}
	for _, m := range mods {
		var started any
		if m.StartedAt != nil {
			started = m.StartedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO job_module (job_id, module_key, name, ord, status, started_at, duration_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, jobID, m.Key, m.Name, m.Ord, m.Status, started, m.DurationMs); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListJobModules returns a job's modules in reactor order.
func (s *Store) ListJobModules(ctx context.Context, jobID int64) ([]JobModule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT module_key, name, ord, status, started_at, COALESCE(duration_ms, 0)
		FROM job_module WHERE job_id = ? ORDER BY ord`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []JobModule{}
	for rows.Next() {
		var m JobModule
		var started sql.NullString
		if err := rows.Scan(&m.Key, &m.Name, &m.Ord, &m.Status, &started, &m.DurationMs); err != nil {
			return nil, err
		}
		m.StartedAt = parseNullTime(started)
		out = append(out, m)
	}
	return out, rows.Err()
}

// JobProblem is a problem found in a job's log: a compile error, a failed test, a failed goal.
type JobProblem struct {
	Kind    string `json:"kind"`
	Module  string `json:"module"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Message string `json:"message"`
	LogLine int    `json:"logLine"`
}

// SaveJobProblems replaces a job's problems.
func (s *Store) SaveJobProblems(ctx context.Context, jobID int64, probs []JobProblem) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM job_problem WHERE job_id = ?`, jobID); err != nil {
		return err
	}
	for _, p := range probs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO job_problem (job_id, module_key, kind, file, line, message, log_line)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, jobID, p.Module, p.Kind, p.File, p.Line, p.Message, p.LogLine); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListJobProblems returns a job's problems in log order.
func (s *Store) ListJobProblems(ctx context.Context, jobID int64) ([]JobProblem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, COALESCE(module_key, ''), COALESCE(file, ''), COALESCE(line, 0), message, COALESCE(log_line, 0)
		FROM job_problem WHERE job_id = ? ORDER BY id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []JobProblem{}
	for rows.Next() {
		var p JobProblem
		if err := rows.Scan(&p.Kind, &p.Module, &p.File, &p.Line, &p.Message, &p.LogLine); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
