package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/binarycat0/buildyard/internal/jdk"
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
		t.Fatal("empty executable should reset to the default")
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

func TestJDKSyncAndSettings(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	j21 := jdk.Info{Home: "/jdk/21", FoundAt: "/opt/jdk21", Label: "openjdk@21 21.0.8 (Homebrew)", Version: "21.0.8", Major: 21, Vendor: "Temurin", Source: "sdkman"}
	j17 := jdk.Info{Home: "/jdk/17", Version: "17.0.12", Major: 17, Source: "macos"}
	exists := func(string) bool { return true }

	r, err := s.SyncDetectedJDKs(ctx, []jdk.Info{j21, j17}, exists)
	if err != nil || r.Added != 2 || r.Missing != 0 {
		t.Fatalf("first sync: %+v %v", r, err)
	}
	manual, err := s.AddJDK(ctx, jdk.Info{Home: "/opt/graal", Version: "21.0.2", Major: 21}, "Graal")
	if err != nil || manual.Source != "manual" || manual.Name != "Graal" {
		t.Fatalf("manual: %+v %v", manual, err)
	}
	if _, err := s.AddJDK(ctx, j21, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate home: %v", err)
	}

	list, _ := s.ListJDKs(ctx)
	if list[0].Label != "openjdk@21 21.0.8 (Homebrew)" || list[0].FoundAt != "/opt/jdk21" {
		t.Fatalf("label/foundAt not stored: %+v", list[0])
	}
	renamed, _ := s.RenameJDK(ctx, list[0].ID, "main")

	// 17 disappeared; manual one still exists on disk; 21 still there keeps its label.
	r, err = s.SyncDetectedJDKs(ctx, []jdk.Info{j21}, exists)
	if err != nil || r.Added != 0 || r.Missing != 1 {
		t.Fatalf("second sync: %+v %v", r, err)
	}
	list, _ = s.ListJDKs(ctx)
	byHome := map[string]JDK{}
	for _, j := range list {
		byHome[j.Home] = j
	}
	if !byHome["/jdk/17"].Missing || byHome["/opt/graal"].Missing || byHome[renamed.Home].Name != "main" {
		t.Fatalf("after sync: %+v", list)
	}
	if list[len(list)-1].Home != "/jdk/17" {
		t.Fatalf("missing JDKs should sort last: %+v", list)
	}

	// Settings default, save, and dangling JDK reference.
	st, _ := s.GetSettings(ctx)
	if st.DefaultExecutable != "auto" || st.DefaultJdkID != nil {
		t.Fatalf("defaults: %+v", st)
	}
	id := byHome["/jdk/17"].ID
	if err := s.SaveSettings(ctx, Settings{DefaultExecutable: "mvnd", DefaultJdkID: &id}); err != nil {
		t.Fatal(err)
	}
	st, _ = s.GetSettings(ctx)
	if st.DefaultExecutable != "mvnd" || st.DefaultJdkID == nil || *st.DefaultJdkID != id {
		t.Fatalf("saved: %+v", st)
	}

	// A project pointing at the JDK falls back to default when it is deleted.
	p, _ := s.CreateProject(ctx, Project{Name: "p", RootPath: "/p", BuildTool: "maven"})
	p, _ = s.UpdateProject(ctx, p.ID, ProjectPatch{JdkID: &id})
	if p.JdkID == nil || *p.JdkID != id {
		t.Fatalf("project jdk: %+v", p.JdkID)
	}
	s.DeleteJDK(ctx, id)
	p, _ = s.GetProject(ctx, p.ID)
	st, _ = s.GetSettings(ctx)
	if p.JdkID != nil || st.DefaultJdkID != nil {
		t.Fatalf("after delete: project %v default %v", p.JdkID, st.DefaultJdkID)
	}
	zero := int64(0)
	if p, _ = s.UpdateProject(ctx, p.ID, ProjectPatch{JdkID: &zero}); p.JdkID != nil {
		t.Fatal("zero should reset")
	}
}
