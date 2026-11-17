// Package maven holds the Maven adapter: choosing the executable now, building commands later.
package maven

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Executable modes stored in project/global settings.
const (
	ModeAuto    = "auto"    // wrapper if the project has one, else mvnd, else mvn
	ModeWrapper = "wrapper" // the project's own ./mvnw
	ModeMvnd    = "mvnd"
	ModeMvn     = "mvn"
	// Any absolute path is a custom executable.
)

// ValidMode reports whether m is a known mode or an absolute path.
func ValidMode(m string) bool {
	switch m {
	case ModeAuto, ModeWrapper, ModeMvnd, ModeMvn:
		return true
	}
	return filepath.IsAbs(m)
}

// Wrapper describes a project's Maven wrapper.
type Wrapper struct {
	Path         string `json:"path"`
	MavenVersion string `json:"mavenVersion"` // from .mvn/wrapper/maven-wrapper.properties, may be empty
}

// Tools are the Maven launchers found on this machine.
type Tools struct {
	Mvn  string `json:"mvn"`  // "" if not found
	Mvnd string `json:"mvnd"` // "" if not found
}

// Resolved is the executable a run will use, and why.
type Resolved struct {
	Mode   string `json:"mode"`   // the mode that was asked for
	Kind   string `json:"kind"`   // wrapper | mvnd | mvn | custom
	Path   string `json:"path"`   // argv[0]
	Reason string `json:"reason"` // human-readable explanation
}

// LookPath finds a program in PATH and in the usual install dirs that a launchd/systemd
// service PATH tends to miss (Homebrew, sdkman).
func LookPath(name, home string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	dirs := []string{
		"/opt/homebrew/bin", "/usr/local/bin", "/home/linuxbrew/.linuxbrew/bin",
		filepath.Join(home, ".sdkman/candidates/maven/current/bin"),
		filepath.Join(home, ".sdkman/candidates/mvnd/current/bin"),
	}
	for _, d := range dirs {
		if p := filepath.Join(d, name); isExecutable(p) {
			return p
		}
	}
	return ""
}

// FindTools locates mvn and mvnd.
func FindTools(home string) Tools {
	return Tools{Mvn: LookPath("mvn", home), Mvnd: LookPath("mvnd", home)}
}

// FindWrapper returns the project's ./mvnw, or nil if there is none.
func FindWrapper(root string) *Wrapper {
	p := filepath.Join(root, "mvnw")
	if !isFile(p) {
		return nil
	}
	return &Wrapper{Path: p, MavenVersion: wrapperMavenVersion(root)}
}

var distVersion = regexp.MustCompile(`apache-maven-([0-9][^/]*?)-bin\.(zip|tar\.gz)`)

func wrapperMavenVersion(root string) string {
	f, err := os.Open(filepath.Join(root, ".mvn/wrapper/maven-wrapper.properties"))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if ok && strings.TrimSpace(k) == "distributionUrl" {
			if m := distVersion.FindStringSubmatch(v); m != nil {
				return m[1]
			}
		}
	}
	return ""
}

// ErrNotAvailable means the requested executable is not installed or not present in the project.
var ErrNotAvailable = errors.New("executable not available")

// Resolve picks the executable for a project root and mode, given what is installed.
func Resolve(mode, root string, tools Tools) (Resolved, error) {
	if mode == "" {
		mode = ModeAuto
	}
	r := Resolved{Mode: mode}
	w := FindWrapper(root)
	switch mode {
	case ModeAuto:
		switch {
		case w != nil:
			r.Kind, r.Path, r.Reason = ModeWrapper, w.Path, "в проекте есть ./mvnw"
		case tools.Mvnd != "":
			r.Kind, r.Path, r.Reason = ModeMvnd, tools.Mvnd, "нет ./mvnw, найден mvnd"
		case tools.Mvn != "":
			r.Kind, r.Path, r.Reason = ModeMvn, tools.Mvn, "нет ./mvnw и mvnd, найден mvn"
		default:
			return r, fmt.Errorf("%w: no ./mvnw in the project and neither mvnd nor mvn found", ErrNotAvailable)
		}
	case ModeWrapper:
		if w == nil {
			return r, fmt.Errorf("%w: the project has no ./mvnw", ErrNotAvailable)
		}
		r.Kind, r.Path, r.Reason = ModeWrapper, w.Path, "выбран враппер проекта"
	case ModeMvnd:
		if tools.Mvnd == "" {
			return r, fmt.Errorf("%w: mvnd not found (brew install mvnd)", ErrNotAvailable)
		}
		r.Kind, r.Path, r.Reason = ModeMvnd, tools.Mvnd, "выбран mvnd"
	case ModeMvn:
		if tools.Mvn == "" {
			return r, fmt.Errorf("%w: mvn not found", ErrNotAvailable)
		}
		r.Kind, r.Path, r.Reason = ModeMvn, tools.Mvn, "выбран mvn"
	default:
		if !filepath.IsAbs(mode) {
			return r, fmt.Errorf("unknown executable %q", mode)
		}
		if !isExecutable(mode) {
			return r, fmt.Errorf("%w: %s is not an executable file", ErrNotAvailable, mode)
		}
		r.Kind, r.Path, r.Reason = "custom", mode, "свой путь"
	}
	return r, nil
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func isExecutable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
}
