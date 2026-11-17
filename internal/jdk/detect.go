// Package jdk finds installed JDKs and reads their version from the `release` file.
package jdk

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// Info describes one JDK home.
type Info struct {
	Home    string `json:"home"`    // canonical path (symlinks resolved)
	Version string `json:"version"` // e.g. "21.0.8"
	Major   int    `json:"major"`   // e.g. 21
	Vendor  string `json:"vendor"`  // IMPLEMENTOR from release, may be empty
	Arch    string `json:"arch"`    // OS_ARCH from release, e.g. "aarch64"
	Source  string `json:"source"`  // where it was found: macos, homebrew, sdkman, jenv, ...
}

// ErrNotJDK is returned when a directory has no bin/java.
var ErrNotJDK = errors.New("not a JDK home: bin/java not found")

// Detector looks for JDKs in the usual install locations. Fields are injectable for tests.
type Detector struct {
	Home     string // user home directory
	GOOS     string
	JavaHome string // $JAVA_HOME, checked too
}

// NewDetector returns a detector for the current user and OS.
func NewDetector() Detector {
	home, _ := os.UserHomeDir()
	return Detector{Home: home, GOOS: runtime.GOOS, JavaHome: os.Getenv("JAVA_HOME")}
}

type pattern struct{ glob, source string }

func (d Detector) patterns() []pattern {
	h := d.Home
	ps := []pattern{
		{filepath.Join(h, ".sdkman/candidates/java/*"), "sdkman"},
		{filepath.Join(h, ".jenv/versions/*"), "jenv"},
		{filepath.Join(h, ".asdf/installs/java/*"), "asdf"},
		{filepath.Join(h, ".local/share/mise/installs/java/*"), "mise"},
		{filepath.Join(h, ".gradle/jdks/*"), "gradle"},
	}
	if d.GOOS == "darwin" {
		ps = append(ps,
			pattern{"/Library/Java/JavaVirtualMachines/*/Contents/Home", "macos"},
			pattern{filepath.Join(h, "Library/Java/JavaVirtualMachines/*/Contents/Home"), "macos"},
			pattern{"/opt/homebrew/opt/openjdk*/libexec/openjdk.jdk/Contents/Home", "homebrew"},
			pattern{"/usr/local/opt/openjdk*/libexec/openjdk.jdk/Contents/Home", "homebrew"},
			pattern{filepath.Join(h, ".gradle/jdks/*/Contents/Home"), "gradle"},
		)
	} else {
		ps = append(ps,
			pattern{"/usr/lib/jvm/*", "system"},
			pattern{"/usr/java/*", "system"},
			pattern{"/opt/java/*", "system"},
			pattern{"/home/linuxbrew/.linuxbrew/opt/openjdk*/libexec", "homebrew"},
		)
	}
	if d.JavaHome != "" {
		ps = append(ps, pattern{d.JavaHome, "JAVA_HOME"})
	}
	return ps
}

// Detect returns every JDK found, deduplicated by canonical path, newest major version first.
func (d Detector) Detect() []Info {
	seen := map[string]bool{}
	var out []Info
	for _, p := range d.patterns() {
		matches, _ := filepath.Glob(p.glob)
		sort.Strings(matches)
		for _, m := range matches {
			if filepath.Base(m) == "current" { // sdkman/asdf symlink to another entry
				continue
			}
			info, err := Inspect(m)
			if err != nil || seen[info.Home] {
				continue
			}
			seen[info.Home] = true
			info.Source = p.source
			out = append(out, info)
		}
	}
	Sort(out)
	return out
}

// Sort orders JDKs by major version (newest first), then version, then path.
func Sort(list []Info) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Major != b.Major {
			return a.Major > b.Major
		}
		if a.Version != b.Version {
			return a.Version > b.Version
		}
		return a.Home < b.Home
	})
}

// Inspect validates a JDK home and reads its release file. On macOS a bundle path
// (…/foo.jdk) is accepted and resolved to …/foo.jdk/Contents/Home.
func Inspect(dir string) (Info, error) {
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	if isFile(filepath.Join(dir, "Contents/Home/bin/java")) {
		dir = filepath.Join(dir, "Contents/Home")
	}
	if !isFile(filepath.Join(dir, "bin/java")) {
		return Info{}, ErrNotJDK
	}
	info := Info{Home: filepath.Clean(dir)}
	rel := readRelease(filepath.Join(dir, "release"))
	info.Version = rel["JAVA_VERSION"]
	info.Vendor = rel["IMPLEMENTOR"]
	info.Arch = rel["OS_ARCH"]
	info.Major = Major(info.Version)
	return info, nil
}

// Major returns the feature version: "21.0.8" → 21, "1.8.0_402" → 8, "" → 0.
func Major(v string) int {
	parts := strings.FieldsFunc(v, func(r rune) bool { return r == '.' || r == '_' || r == '-' || r == '+' })
	if len(parts) == 0 {
		return 0
	}
	n, _ := strconv.Atoi(parts[0])
	if n == 1 && len(parts) > 1 {
		n, _ = strconv.Atoi(parts[1])
	}
	return n
}

func readRelease(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if ok {
			out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return out
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
