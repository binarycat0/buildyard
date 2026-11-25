package project

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestWorktrees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	base := resolve(t.TempDir())
	repo := filepath.Join(base, "repo")
	sub := filepath.Join(repo, "app")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(sub, "pom.xml"), []byte("<project/>"), 0o644)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-qm", "init")
	git(t, repo, "worktree", "add", "-q", "-b", "feature", filepath.Join(base, "wt"))

	got := Worktrees(context.Background(), sub)
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Dir != sub || got[0].Branch != "main" || !got[0].Main || got[0].Head == "" {
		t.Errorf("main worktree: %+v", got[0])
	}
	if got[1].Dir != filepath.Join(base, "wt", "app") || got[1].Branch != "feature" || got[1].Main {
		t.Errorf("linked worktree: %+v", got[1])
	}
	if Worktrees(context.Background(), base) != nil {
		t.Error("a directory outside git has no worktrees")
	}
}

func TestParseWorktreesSkipsBare(t *testing.T) {
	out := "worktree /r\nbare\n\nworktree /w\nHEAD abc\ndetached\n\n"
	got := parseWorktrees([]byte(out), ".", "/w")
	if len(got) != 1 || got[0].Dir != "/w" || got[0].Branch != "" || got[0].Head != "abc" || !got[0].Main {
		t.Fatalf("got %+v", got)
	}
}
