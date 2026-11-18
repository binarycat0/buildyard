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
	FoundAt string `json:"foundAt"` // path where it was found, if different from Home (e.g. a Homebrew opt/ or jenv symlink)
	Label   string `json:"label"`   // name derived from the install location, e.g. "openjdk@21 21.0.8 (Homebrew)"
	Version string `json:"version"` // e.g. "21.0.8"
	Major   int    `json:"major"`   // e.g. 21
	Vendor  string `json:"vendor"`  // IMPLEMENTOR from release, may be empty
	Arch    string `json:"arch"`    // OS_ARCH from release, e.g. "aarch64"
	Source  string `json:"source"`  // where it was found: macos, homebrew, sdkman, jenv, ...
}

// ErrNotJDK is returned when a directory has no bin/java.
var ErrNotJDK = errors.New("not a JDK home: bin/java not found")

// Detector asks JDK managers (macOS java_home, Homebrew, sdkman, jenv, asdf, mise) and then
// scans the usual install locations. Fields are injectable for tests.
type Detector struct {
	Home     string // user home directory
	GOOS     string
	JavaHome string // $JAVA_HOME, checked too

	// Optional hooks for tests; nil means the real implementation.
	Run      Runner
	Getenv   func(string) string
	LookPath func(string) string // returns "" when not found
	// SystemRoot prefixes system-wide paths (/Library, /opt, /usr); tests point it at a temp dir
	// so the machine's real JDKs are never picked up. "" means "/".
	SystemRoot string
}

func (d Detector) sys(p string) string {
	if d.SystemRoot == "" {
		return p
	}
	return filepath.Join(d.SystemRoot, p)
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
			pattern{d.sys("/Library/Java/JavaVirtualMachines/*/Contents/Home"), "macos"},
			pattern{filepath.Join(h, "Library/Java/JavaVirtualMachines/*/Contents/Home"), "macos"},
			pattern{d.sys("/opt/homebrew/opt/openjdk*/libexec/openjdk.jdk/Contents/Home"), "homebrew"},
			pattern{d.sys("/usr/local/opt/openjdk*/libexec/openjdk.jdk/Contents/Home"), "homebrew"},
			// Kegs that are installed but not linked into opt/ (older versions of a formula).
			pattern{d.sys("/opt/homebrew/Cellar/openjdk*/*/libexec/openjdk.jdk/Contents/Home"), "homebrew"},
			pattern{d.sys("/usr/local/Cellar/openjdk*/*/libexec/openjdk.jdk/Contents/Home"), "homebrew"},
			pattern{filepath.Join(h, ".gradle/jdks/*/Contents/Home"), "gradle"},
		)
	} else {
		ps = append(ps,
			pattern{d.sys("/usr/lib/jvm/*"), "system"},
			pattern{d.sys("/usr/java/*"), "system"},
			pattern{d.sys("/opt/java/*"), "system"},
			pattern{d.sys("/home/linuxbrew/.linuxbrew/opt/openjdk*/libexec"), "homebrew"},
			pattern{d.sys("/home/linuxbrew/.linuxbrew/Cellar/openjdk*/*/libexec"), "homebrew"},
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
	// Managers first: their answers are authoritative and give the best "found at" paths.
	for _, p := range append(d.fromManagers(), d.patterns()...) {
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
	found := filepath.Clean(dir)
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
	if info.Version == "" {
		// No release file (or no JAVA_VERSION in it): ask the JDK itself.
		if p, err := probe(filepath.Join(dir, "bin/java")); err == nil {
			info.Version = p["java.version"]
			if info.Vendor == "" {
				info.Vendor = p["java.vendor"]
			}
			if info.Arch == "" {
				info.Arch = p["os.arch"]
			}
		}
	}
	info.Major = Major(info.Version)
	if found != info.Home && found+"/Contents/Home" != info.Home {
		info.FoundAt = found
	}
	info.Label = Label(info.Home)
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
