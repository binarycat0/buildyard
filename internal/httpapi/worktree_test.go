package httpapi

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/binarycat0/buildyard/internal/project"
	"github.com/binarycat0/buildyard/internal/store"
)

func TestRunInWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := mavenProject(t)
	wt := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-wt")
	t.Cleanup(func() { os.RemoveAll(wt) })
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"commit", "-qm", "init"}, {"worktree", "add", "-q", "-b", "feature", wt}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	h, _ := newServer(t)
	var p store.Project
	decodeInto(do(t, h, req{method: "POST", path: "/api/projects", body: `{"rootPath":"` + root + `"}`}), &p)
	pid := strconv.FormatInt(p.ID, 10)

	var wts []project.Worktree
	decodeInto(do(t, h, req{method: "GET", path: "/api/projects/" + pid + "/worktrees"}), &wts)
	if len(wts) != 2 || wts[1].Dir != wt || wts[1].Branch != "feature" {
		t.Fatalf("worktrees: %+v", wts)
	}
	if w := do(t, h, req{method: "POST", path: "/api/projects/" + pid + "/jobs", body: `{"spec":{"goals":["install"]},"worktree":"/tmp"}`}); w.Code != 400 {
		t.Fatalf("unknown worktree: %d", w.Code)
	}
	var j jobView
	decodeInto(do(t, h, req{method: "POST", path: "/api/projects/" + pid + "/jobs", body: `{"spec":{"goals":["install"]},"worktree":"` + wt + `"}`}), &j)
	j = waitJob(t, h, j.ID)
	if j.Status != store.JobSucceeded || j.Dir != wt || j.Argv[0] != filepath.Join(wt, "mvnw") {
		t.Fatalf("job: %+v", j.Job)
	}
	var re jobView
	decodeInto(do(t, h, req{method: "POST", path: "/api/jobs/" + strconv.FormatInt(j.ID, 10) + "/rerun"}), &re)
	if re = waitJob(t, h, re.ID); re.Dir != wt {
		t.Fatalf("rerun left the worktree: %+v", re.Job)
	}
}
