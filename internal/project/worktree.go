package project

import (
	"bufio"
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Worktree is one git worktree of a project's repository.
type Worktree struct {
	Dir    string `json:"dir"`    // where the project root is in this worktree
	Branch string `json:"branch"` // "" when detached
	Head   string `json:"head"`
	Main   bool   `json:"main"` // the worktree the project was added from
}

// Worktrees lists the git worktrees of the repository that holds root. Dir is root's
// counterpart in each worktree, so a project in a subdirectory of the repository
// maps to the same subdirectory. It returns nil when root is not in a git repository.
func Worktrees(ctx context.Context, root string) []Worktree {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	top, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return nil
	}
	rel, err := filepath.Rel(resolve(strings.TrimSpace(string(top))), resolve(root))
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = "."
	}
	out, err := exec.CommandContext(ctx, "git", "-C", root, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil
	}
	return parseWorktrees(out, rel, resolve(root))
}

func parseWorktrees(out []byte, rel, root string) []Worktree {
	var list []Worktree
	var cur *Worktree
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		key, val, _ := strings.Cut(sc.Text(), " ")
		switch key {
		case "worktree":
			list = append(list, Worktree{Dir: filepath.Join(resolve(val), rel)})
			cur = &list[len(list)-1]
			cur.Main = cur.Dir == root
		case "HEAD":
			if cur != nil {
				cur.Head = val
			}
		case "branch":
			if cur != nil {
				cur.Branch = strings.TrimPrefix(val, "refs/heads/")
			}
		case "bare", "prunable":
			if cur != nil {
				list = list[:len(list)-1] // nothing to build there
				cur = nil
			}
		}
	}
	return list
}

func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
