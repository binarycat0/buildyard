package project

import (
	"context"
	"os/exec"
	"testing"
)

func TestGitInfo(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if g := GitInfo(ctx, dir); g != nil {
		t.Fatalf("plain dir: %+v", g)
	}
	if err := exec.Command("git", "-C", dir, "init", "-q", "-b", "trunk").Run(); err != nil {
		t.Skip("git not available:", err)
	}
	if g := GitInfo(ctx, dir); g == nil || g.Branch != "trunk" || g.Commit != "" {
		t.Fatalf("fresh repo: %+v", g)
	}
}
