// Package project validates project roots and detects their build tool.
package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var (
	// ErrNotAbsolute means the path given was relative.
	ErrNotAbsolute = errors.New("path must be absolute")
	// ErrNotDir means the path does not exist or is not a directory.
	ErrNotDir = errors.New("path is not a directory")
	// ErrNoBuildFile means no supported build file was found at the root.
	ErrNoBuildFile = errors.New("no pom.xml in this directory")
	// ErrUnsupported means a known but not yet supported build tool was found.
	ErrUnsupported = errors.New("build tool not supported yet")
)

// Root is a validated project root.
type Root struct {
	Path      string // cleaned absolute path, symlinks resolved
	BuildTool string // "maven"
}

// Inspect checks that path is a directory with a build file Buildyard can drive.
func Inspect(path string) (Root, error) {
	if !filepath.IsAbs(path) {
		return Root{}, ErrNotAbsolute
	}
	clean := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		clean = resolved
	}
	st, err := os.Stat(clean)
	if err != nil || !st.IsDir() {
		return Root{}, ErrNotDir
	}
	if isFile(filepath.Join(clean, "pom.xml")) {
		return Root{Path: clean, BuildTool: "maven"}, nil
	}
	for _, f := range []string{"build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts"} {
		if isFile(filepath.Join(clean, f)) {
			return Root{}, fmt.Errorf("%w: gradle", ErrUnsupported)
		}
	}
	return Root{}, ErrNoBuildFile
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}
