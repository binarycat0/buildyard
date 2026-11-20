package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Job statuses.
const (
	JobQueued      = "queued"
	JobRunning     = "running"
	JobSucceeded   = "succeeded"
	JobFailed      = "failed"
	JobCancelled   = "cancelled"
	JobInterrupted = "interrupted" // the daemon stopped while the job was queued or running
)

// Job is one run of a command in a project.
type Job struct {
	ID         int64           `json:"id"`
	ProjectID  int64           `json:"projectId"`
	ProfileID  *int64          `json:"profileId"`
	Spec       json.RawMessage `json:"spec"` // RunSpec snapshot
	Argv       []string        `json:"argv"` // final command
	Status     string          `json:"status"`
	ExitCode   *int            `json:"exitCode"`
	QueuedAt   time.Time       `json:"queuedAt"`
	StartedAt  *time.Time      `json:"startedAt"`
	FinishedAt *time.Time      `json:"finishedAt"`
	GitHead    *string         `json:"gitHead"`
	TraceID    *string         `json:"traceId"` // OpenTelemetry trace, when the job was exported
	LogPath    string          `json:"-"`
	LogLines   int             `json:"logLines"`
}

// Finished reports whether the job reached a final status.
func (j Job) Finished() bool {
	switch j.Status {
	case JobQueued, JobRunning:
		return false
	}
	return true
}

const jobCols = `id, project_id, profile_id, spec_json, argv_json, status, exit_code,
	queued_at, started_at, finished_at, git_head, log_path, log_lines, trace_id`

// nowNano uses fixed-width fractions so job times sort correctly as text.
func nowNano() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z07:00") }

// CreateJob inserts a queued job. logPath may contain "{id}", replaced with the job id.
func (s *Store) CreateJob(ctx context.Context, j Job, logPathFor func(id int64) string) (Job, error) {
	if len(j.Spec) == 0 {
		j.Spec = json.RawMessage("{}")
	}
	argv, _ := json.Marshal(j.Argv)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO job (project_id, profile_id, spec_json, argv_json, status, queued_at, log_path)
		VALUES (?, ?, ?, ?, ?, ?, '')`, j.ProjectID, j.ProfileID, string(j.Spec), string(argv), JobQueued, nowNano())
	if err != nil {
		return Job{}, err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.ExecContext(ctx, `UPDATE job SET log_path = ? WHERE id = ?`, logPathFor(id), id); err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	return s.GetJob(ctx, id)
}

// StartJob marks a job running.
func (s *Store) StartJob(ctx context.Context, id int64, gitHead string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE job SET status = ?, started_at = ?, git_head = ? WHERE id = ?`,
		JobRunning, nowNano(), nullIfEmpty(gitHead), id)
	return err
}

// SetJobTrace records the OpenTelemetry trace a job was exported as.
func (s *Store) SetJobTrace(ctx context.Context, id int64, traceID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE job SET trace_id = ? WHERE id = ?`, nullIfEmpty(traceID), id)
	return err
}

// FinishJob records the final status. exitCode is nil when the process never ran.
func (s *Store) FinishJob(ctx context.Context, id int64, status string, exitCode *int, logLines int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE job SET status = ?, exit_code = ?, finished_at = ?, log_lines = ? WHERE id = ?`,
		status, exitCode, nowNano(), logLines, id)
	return err
}

// InterruptUnfinished marks jobs left queued or running by a previous daemon as interrupted.
func (s *Store) InterruptUnfinished(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE job SET status = ?, finished_at = ? WHERE status IN (?, ?)`,
		JobInterrupted, nowNano(), JobQueued, JobRunning)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// GetJob returns one job.
func (s *Store) GetJob(ctx context.Context, id int64) (Job, error) {
	j, err := scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobCols+` FROM job WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	return j, err
}

// ListJobs returns a project's jobs, newest first.
func (s *Store) ListJobs(ctx context.Context, projectID int64, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobCols+` FROM job WHERE project_id = ?
		ORDER BY queued_at DESC, id DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// PruneJobs deletes a project's finished jobs beyond the newest keep and returns their log paths.
func (s *Store) PruneJobs(ctx context.Context, projectID int64, keep int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, log_path FROM job WHERE project_id = ? AND status NOT IN (?, ?)
		ORDER BY queued_at DESC, id DESC LIMIT -1 OFFSET ?`, projectID, JobQueued, JobRunning, keep)
	if err != nil {
		return nil, err
	}
	var ids []int64
	var paths []string
	for rows.Next() {
		var id int64
		var p string
		if err := rows.Scan(&id, &p); err != nil {
			rows.Close()
			return nil, err
		}
		ids, paths = append(ids, id), append(paths, p)
	}
	rows.Close()
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM job WHERE id = ?`, id); err != nil {
			return nil, err
		}
	}
	return paths, nil
}

func scanJob(r scanner) (Job, error) {
	var (
		j                          Job
		spec, argv, queued         string
		started, finished, gitHead sql.NullString
		traceID                    sql.NullString
		exit                       sql.NullInt64
	)
	if err := r.Scan(&j.ID, &j.ProjectID, &j.ProfileID, &spec, &argv, &j.Status, &exit,
		&queued, &started, &finished, &gitHead, &j.LogPath, &j.LogLines, &traceID); err != nil {
		return Job{}, err
	}
	j.Spec = json.RawMessage(spec)
	_ = json.Unmarshal([]byte(argv), &j.Argv)
	if exit.Valid {
		c := int(exit.Int64)
		j.ExitCode = &c
	}
	j.QueuedAt = parseTime(queued)
	j.StartedAt = parseNullTime(started)
	j.FinishedAt = parseNullTime(finished)
	if gitHead.Valid {
		j.GitHead = &gitHead.String
	}
	if traceID.Valid {
		j.TraceID = &traceID.String
	}
	return j, nil
}
