package httpapi

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/binarycat0/buildyard/internal/store"
)

// Rerun without failed modules leaves out the failed ones and what depends on them.
func TestRerunWithoutFailed(t *testing.T) {
	s, _ := newTestServer(t, t.TempDir())
	h := s.Handler()
	ctx := context.Background()
	root := mavenProject(t)
	var p store.Project
	decodeInto(do(t, h, req{method: "POST", path: "/api/projects", body: `{"rootPath":"` + root + `"}`}), &p)
	mods := []store.Module{
		{Key: "g:core", GroupID: "g", ArtifactID: "core", Name: "Core"},
		{Key: "g:codegen", GroupID: "g", ArtifactID: "codegen", Name: "Codegen", Deps: []string{"g:core"}},
		{Key: "g:it", GroupID: "g", ArtifactID: "it", Name: "IT", Deps: []string{"g:codegen"}},
		{Key: "g:app", GroupID: "g", ArtifactID: "app", Name: "App", Deps: []string{"g:core"}},
		{Key: "x:app", GroupID: "x", ArtifactID: "app", Name: "Other app"},
	}
	if err := s.Store.ReplaceModules(ctx, p.ID, mods, "test"); err != nil {
		t.Fatal(err)
	}
	spec, _ := json.Marshal(map[string]any{"goals": []string{"install"}})
	j, err := s.Store.CreateJob(ctx, store.Job{ProjectID: p.ID, Argv: []string{"x"}, Spec: spec}, func(int64) string { return t.TempDir() + "/l" })
	if err != nil {
		t.Fatal(err)
	}
	s.Store.FinishJob(ctx, j.ID, store.JobFailed, nil, 0)
	id := strconv.FormatInt(j.ID, 10)

	if w := do(t, h, req{method: "POST", path: "/api/jobs/" + id + "/rerun?without=failed"}); w.Code != 400 {
		t.Fatalf("no failed modules: %d %s", w.Code, w.Body)
	}
	// Codegen failed (log-based progress: keyed by name); IT depends on it.
	s.Store.SaveJobModules(ctx, j.ID, []store.JobModule{
		{Key: "Core", Name: "Core", Status: "succeeded"},
		{Key: "Codegen", Name: "Codegen", Status: "failed"},
		{Key: "IT", Name: "IT", Status: "skipped"},
	})
	w := do(t, h, req{method: "POST", path: "/api/jobs/" + id + "/rerun?without=failed"})
	if w.Code != 201 {
		t.Fatalf("rerun: %d %s", w.Code, w.Body)
	}
	var v jobView
	decodeInto(w, &v)
	if got := strings.Join(v.Argv[1:], " "); got != "-B -pl !:codegen,!:it install" {
		t.Fatalf("argv: %s", got)
	}
	waitJob(t, h, v.ID)

	// Extension keys; a non-unique artifactId keeps its groupId.
	s.Store.SaveJobModules(ctx, j.ID, []store.JobModule{{Key: "x:app", Name: "Other app", Status: "failed"}})
	decodeInto(do(t, h, req{method: "POST", path: "/api/jobs/" + id + "/rerun?without=failed"}), &v)
	if got := strings.Join(v.Argv[1:], " "); got != "-B -pl !x:app install" {
		t.Fatalf("argv: %s", got)
	}
	waitJob(t, h, v.ID)
}
