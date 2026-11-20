package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/binarycat0/buildyard/internal/jdk"
	"github.com/pressly/goose/v3"
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
	if mv := st.Tool("maven"); mv.Executable != "auto" || mv.JdkID != nil {
		t.Fatalf("defaults: %+v", st)
	}
	id := byHome["/jdk/17"].ID
	if err := s.SaveSettings(ctx, Settings{Tools: map[string]ToolSettings{"maven": {Executable: "mvnd", JdkID: &id}}}); err != nil {
		t.Fatal(err)
	}
	st, _ = s.GetSettings(ctx)
	if mv := st.Tool("maven"); mv.Executable != "mvnd" || mv.JdkID == nil || *mv.JdkID != id {
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
	if p.JdkID != nil || st.Tool("maven").JdkID != nil {
		t.Fatalf("after delete: project %v default %v", p.JdkID, st.Tool("maven").JdkID)
	}
	zero := int64(0)
	if p, _ = s.UpdateProject(ctx, p.ID, ProjectPatch{JdkID: &zero}); p.JdkID != nil {
		t.Fatal("zero should reset")
	}
}

func TestMigrateFlatSettingsToMaven(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, db, mustSub(migrations, "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 3); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO jdk (id, home, label, name, version, major, vendor, arch, source, detected_at) VALUES (7, '/jdk/21', '', '', '21', 21, '', '', 'manual', '2026-01-01')`,
		`INSERT INTO setting (key, value) VALUES ('defaultExecutable', '"mvnd"'), ('defaultJdkId', '7')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(q, err)
		}
	}
	db.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st, err := s.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if mv := st.Tool("maven"); mv.Executable != "mvnd" || mv.JdkID == nil || *mv.JdkID != 7 {
		t.Fatalf("migrated: %+v", mv)
	}
	var n int
	s.db.QueryRowContext(ctx, `SELECT count(*) FROM setting WHERE key IN ('defaultExecutable','defaultJdkId')`).Scan(&n)
	if n != 0 {
		t.Fatalf("old keys left: %d", n)
	}
}

// A database where a development build applied another migration under number 10
// (an unmerged branch) still gets job.trace_id.
func TestColumnsAddedDespiteForeignMigrationNumber(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "by.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE job DROP COLUMN trace_id`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE project ADD COLUMN task_defaults_json TEXT`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := s.CreateProject(ctx, Project{Name: "p", RootPath: "/tmp/p", BuildTool: "maven"})
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.CreateJob(ctx, Job{ProjectID: p.ID, Argv: []string{"mvn"}}, func(int64) string { return "/tmp/x.log" })
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetJobTrace(ctx, j.ID, "abc"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetJob(ctx, j.ID)
	if err != nil || got.TraceID == nil || *got.TraceID != "abc" {
		t.Fatalf("trace id: %v %v", got.TraceID, err)
	}
}
