package project

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// Git describes the checkout a project root is in.
type Git struct {
	Branch string `json:"branch"` // current branch; "" when HEAD is detached
	Commit string `json:"commit"` // short HEAD commit; "" before the first commit
}

// GitInfo returns the git branch of dir, or nil when dir is not in a git work tree or git is missing.
func GitInfo(ctx context.Context, dir string) *Git {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	git := func(args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
		return strings.TrimSpace(string(out)), err
	}
	if inside, err := git("rev-parse", "--is-inside-work-tree"); err != nil || inside != "true" {
		return nil
	}
	g := &Git{}
	g.Branch, _ = git("symbolic-ref", "--quiet", "--short", "HEAD")
	g.Commit, _ = git("rev-parse", "--short", "HEAD")
	return g
}
