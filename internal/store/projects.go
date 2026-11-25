package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Project is a build root registered with the daemon.
type Project struct {
	ID          int64             `json:"id"`
	Name        string            `json:"name"`
	RootPath    string            `json:"rootPath"`
	BuildTool   string            `json:"buildTool"`
	Executable  *string           `json:"executable"` // nil = global default; else auto|wrapper|mvnd|mvn|/abs/path
	JdkID       *int64            `json:"jdkId"`      // project JDK; nil = none set
	ToolJdkID   *int64            `json:"toolJdkId"`  // JDK for the build tool; nil = project JDK, then the tool's global default
	Env         map[string]string `json:"env"`
	SkipPresets json.RawMessage   `json:"skipPresets"`
	Resources   json.RawMessage   `json:"resources"` // default CPU, memory and test JVMs for runs
	CreatedAt   time.Time         `json:"createdAt"`
	LastJob     *JobSummary       `json:"lastJob"`
}

// JobSummary is the short form of a job shown on project cards.
type JobSummary struct {
	ID         int64      `json:"id"`
	Status     string     `json:"status"`
	QueuedAt   time.Time  `json:"queuedAt"`
	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

// ProjectPatch holds the fields PATCH /api/projects/{id} may change. Nil means "leave as is".
type ProjectPatch struct {
	Name        *string            `json:"name"`
	RootPath    *string            `json:"rootPath"`
	Executable  *string            `json:"executable"` // "" resets to the global default
	JdkID       *int64             `json:"jdkId"`      // 0 clears the project JDK
	ToolJdkID   *int64             `json:"toolJdkId"`  // 0 resets to "use the project JDK"
	Env         *map[string]string `json:"env"`
	SkipPresets *json.RawMessage   `json:"skipPresets"`
	Resources   *json.RawMessage   `json:"resources"`
}

const projectCols = `p.id, p.name, p.root_path, p.build_tool, p.executable, p.jdk_id, p.tool_jdk_id,
	p.env_json, p.skip_presets_json, p.resources_json, p.created_at,
	j.id, j.status, j.queued_at, j.started_at, j.finished_at`

const projectFrom = ` FROM project p LEFT JOIN job j ON j.id = (
	SELECT id FROM job WHERE project_id = p.id ORDER BY queued_at DESC, id DESC LIMIT 1)`

// CreateProject inserts a project. RootPath must be unique.
func (s *Store) CreateProject(ctx context.Context, p Project) (Project, error) {
	if p.Env == nil {
		p.Env = map[string]string{}
	}
	if len(p.SkipPresets) == 0 {
		p.SkipPresets = json.RawMessage("[]")
	}
	env, _ := json.Marshal(p.Env)
	res, err := s.db.ExecContext(ctx, `INSERT INTO project
		(name, root_path, build_tool, executable, jdk_id, env_json, skip_presets_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Name, p.RootPath, p.BuildTool, p.Executable, p.JdkID, string(env), string(p.SkipPresets), now())
	if err != nil {
		return Project{}, mapErr(err)
	}
	id, _ := res.LastInsertId()
	return s.GetProject(ctx, id)
}

// GetProject returns one project with its last job.
func (s *Store) GetProject(ctx context.Context, id int64) (Project, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+projectCols+projectFrom+` WHERE p.id = ?`, id)
	p, err := scanProject(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	return p, err
}

// ListProjects returns all projects ordered by name.
func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+projectCols+projectFrom+` ORDER BY p.name COLLATE NOCASE, p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateProject applies a patch. An empty Executable or a zero JdkID resets it to the global default.
func (s *Store) UpdateProject(ctx context.Context, id int64, patch ProjectPatch) (Project, error) {
	var sets []string
	var args []any
	if patch.Name != nil {
		sets, args = append(sets, "name = ?"), append(args, *patch.Name)
	}
	if patch.RootPath != nil {
		sets, args = append(sets, "root_path = ?"), append(args, *patch.RootPath)
	}
	if patch.Executable != nil {
		sets, args = append(sets, "executable = ?"), append(args, nullIfEmpty(*patch.Executable))
	}
	if patch.JdkID != nil {
		sets, args = append(sets, "jdk_id = ?"), append(args, nullIfZero(*patch.JdkID))
	}
	if patch.ToolJdkID != nil {
		sets, args = append(sets, "tool_jdk_id = ?"), append(args, nullIfZero(*patch.ToolJdkID))
	}
	if patch.Env != nil {
		env := *patch.Env
		if env == nil {
			env = map[string]string{}
		}
		b, _ := json.Marshal(env)
		sets, args = append(sets, "env_json = ?"), append(args, string(b))
	}
	if patch.SkipPresets != nil {
		sets, args = append(sets, "skip_presets_json = ?"), append(args, string(*patch.SkipPresets))
	}
	if patch.Resources != nil {
		sets, args = append(sets, "resources_json = ?"), append(args, string(*patch.Resources))
	}
	if len(sets) > 0 {
		args = append(args, id)
		res, err := s.db.ExecContext(ctx, `UPDATE project SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
		if err != nil {
			return Project{}, mapErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return Project{}, ErrNotFound
		}
	}
	return s.GetProject(ctx, id)
}

// DeleteProject removes a project and, through ON DELETE CASCADE, everything that belongs to it.
func (s *Store) DeleteProject(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM project WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type scanner interface{ Scan(...any) error }

func scanProject(r scanner) (Project, error) {
	var (
		p                                  Project
		env, presets, res, created         string
		jobID                              sql.NullInt64
		jobStatus, queued, started, finish sql.NullString
	)
	if err := r.Scan(&p.ID, &p.Name, &p.RootPath, &p.BuildTool, &p.Executable, &p.JdkID, &p.ToolJdkID,
		&env, &presets, &res, &created, &jobID, &jobStatus, &queued, &started, &finish); err != nil {
		return Project{}, err
	}
	if err := json.Unmarshal([]byte(env), &p.Env); err != nil || p.Env == nil {
		p.Env = map[string]string{}
	}
	p.SkipPresets = json.RawMessage(presets)
	p.Resources = json.RawMessage(res)
	p.CreatedAt = parseTime(created)
	if jobID.Valid {
		p.LastJob = &JobSummary{
			ID: jobID.Int64, Status: jobStatus.String, QueuedAt: parseTime(queued.String),
			StartedAt: parseNullTime(started), FinishedAt: parseNullTime(finish),
		}
	}
	return p, nil
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func parseNullTime(s sql.NullString) *time.Time {
	if !s.Valid {
		return nil
	}
	t := parseTime(s.String)
	return &t
}

func nullIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func mapErr(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return ErrConflict
	}
	return err
}

func nullIfZero(id int64) any {
	if id > 0 {
		return id
	}
	return nil
}
