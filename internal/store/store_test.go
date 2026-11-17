package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "buildyard.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestProjectCRUDAndReopen(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)

	p, err := s.CreateProject(ctx, Project{Name: "demo", RootPath: "/src/demo", BuildTool: "maven"})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID == 0 || p.Executable != nil || string(p.SkipPresets) != "[]" || p.LastJob != nil {
		t.Fatalf("unexpected created project: %+v", p)
	}

	if _, err := s.CreateProject(ctx, Project{Name: "dup", RootPath: "/src/demo", BuildTool: "maven"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate root: got %v", err)
	}

	exe, env := "./mvnw", map[string]string{"MAVEN_OPTS": "-Xmx4g"}
	presets := json.RawMessage(`[{"name":"no-ui","args":["-Dskip.ui"]}]`)
	p, err = s.UpdateProject(ctx, p.ID, ProjectPatch{Executable: &exe, Env: &env, SkipPresets: &presets})
	if err != nil {
		t.Fatal(err)
	}
	if *p.Executable != "./mvnw" || p.Env["MAVEN_OPTS"] != "-Xmx4g" {
		t.Fatalf("patch not applied: %+v", p)
	}
	empty := ""
	if p, _ = s.UpdateProject(ctx, p.ID, ProjectPatch{Executable: &empty}); p.Executable != nil {
		t.Fatal("empty executable should reset to auto")
	}

	// Survives a reopen (and migrations are idempotent).
	s.Close()
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	list, err := s2.ListProjects(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "demo" || list[0].Env["MAVEN_OPTS"] != "-Xmx4g" {
		t.Fatalf("after reopen: %v %+v", err, list)
	}

	if err := s2.DeleteProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.GetProject(ctx, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted: %v", err)
	}
	if err := s2.DeleteProject(ctx, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestLastJobIsJoined(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	p, _ := s.CreateProject(ctx, Project{Name: "a", RootPath: "/a", BuildTool: "maven"})
	for i, st := range []string{"failed", "succeeded"} {
		_, err := s.db.ExecContext(ctx, `INSERT INTO job (project_id, spec_json, argv_json, status, queued_at, log_path)
			VALUES (?, '{}', '[]', ?, ?, 'x.log')`, p.ID, st, "2026-11-17T10:0"+string(rune('0'+i))+":00Z")
		if err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.GetProject(ctx, p.ID)
	if got.LastJob == nil || got.LastJob.Status != "succeeded" {
		t.Fatalf("last job: %+v", got.LastJob)
	}
}
