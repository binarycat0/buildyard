package toolconfig

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Tool is an installed Gradle or Bazel launcher. Same shape as maven.InstalledTool so the
// Settings page shows all build tools alike.
type Tool struct {
	ID           string `json:"id"`   // gradle | bazel | bazelisk
	Name         string `json:"name"` // display name
	Installed    bool   `json:"installed"`
	Path         string `json:"path"`
	ResolvedPath string `json:"resolvedPath"`
	Version      string `json:"version"`
	MavenVersion string `json:"mavenVersion"` // always empty; keeps the JSON shape of maven.InstalledTool
	InstallHint  string `json:"installHint"`
}

// SystemBinDirs are searched when a tool is not on PATH (a launchd/systemd service PATH
// misses Homebrew). A variable so tests can leave the machine's own tools out.
var SystemBinDirs = []string{"/opt/homebrew/bin", "/usr/local/bin", "/home/linuxbrew/.linuxbrew/bin"}

func lookPath(name, home string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	dirs := append(append([]string(nil), SystemBinDirs...), filepath.Join(home, ".sdkman/candidates", name, "current/bin"))
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0 {
			return p
		}
	}
	return ""
}

var (
	gradleJar = regexp.MustCompile(`^gradle-(?:launcher|core-api|core|base-services|gradle-cli)-(\d+\.\d+[\w.\-]*?)\.jar$`)
	gradleDir = regexp.MustCompile(`(?:^|/)gradle[-/](\d+\.\d+[\w.\-]*?)(?:-(?:bin|all))?(?:/|$)`)
	bazelDir  = regexp.MustCompile(`(?:^|/)(?:bazel|bazelisk)[-/@]?(\d+\.\d+[\w.\-]*?)(?:/|$)`)
)

// DetectTools finds gradle, bazel and bazelisk and reads their versions from the install
// files or directory names, without running them (Gradle starts a JVM, bazelisk downloads).
func DetectTools(home string) []Tool {
	gradle := Tool{ID: "gradle", Name: "Gradle", InstallHint: "brew install gradle (only needed for projects without ./gradlew)"}
	if p := lookPath("gradle", home); p != "" {
		gradle.Installed, gradle.Path, gradle.ResolvedPath = true, p, resolve(p)
		gradle.Version = gradleVersion(gradle.ResolvedPath)
	}
	bazel := Tool{ID: "bazel", Name: "Bazel", InstallHint: "brew install bazelisk (installs bazel as a bazelisk launcher)"}
	if p := lookPath("bazel", home); p != "" {
		bazel.Installed, bazel.Path, bazel.ResolvedPath = true, p, resolve(p)
		if strings.Contains(filepath.Base(bazel.ResolvedPath), "bazelisk") || strings.Contains(bazel.ResolvedPath, "/bazelisk/") {
			bazel.Name = "Bazel (bazelisk launcher)"
		} else if m := bazelDir.FindStringSubmatch(filepath.ToSlash(bazel.ResolvedPath)); m != nil {
			bazel.Version = m[1]
		}
	}
	bazelisk := Tool{ID: "bazelisk", Name: "Bazelisk", InstallHint: "brew install bazelisk"}
	if p := lookPath("bazelisk", home); p != "" {
		bazelisk.Installed, bazelisk.Path, bazelisk.ResolvedPath = true, p, resolve(p)
		if m := bazelDir.FindStringSubmatch(filepath.ToSlash(bazelisk.ResolvedPath)); m != nil {
			bazelisk.Version = m[1]
		}
	}
	return []Tool{gradle, bazel, bazelisk}
}

func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func gradleVersion(bin string) string {
	dir := filepath.Dir(bin)
	for i := 0; i < 4; i++ {
		for _, lib := range []string{"lib", "libexec/lib"} {
			entries, err := os.ReadDir(filepath.Join(dir, lib))
			if err != nil {
				continue
			}
			var vs []string
			for _, e := range entries {
				if m := gradleJar.FindStringSubmatch(e.Name()); m != nil {
					vs = append(vs, m[1])
				}
			}
			if len(vs) > 0 {
				sort.Strings(vs)
				return vs[len(vs)-1]
			}
		}
		dir = filepath.Dir(dir)
	}
	if m := gradleDir.FindStringSubmatch(filepath.ToSlash(bin)); m != nil {
		return m[1]
	}
	return ""
}

var gradleDist = regexp.MustCompile(`gradle-(\d+\.\d+[\w.\-]*?)-(?:bin|all)\.zip`)

// GradleWrapperVersion returns the Gradle version a project's ./gradlew pins, "" if there
// is no wrapper, "?" if it is there but the version is unreadable.
func GradleWrapperVersion(root string) string {
	if _, err := os.Stat(filepath.Join(root, "gradlew")); err != nil {
		return ""
	}
	f, err := os.Open(filepath.Join(root, "gradle/wrapper/gradle-wrapper.properties"))
	if err != nil {
		return "?"
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if ok && strings.TrimSpace(k) == "distributionUrl" {
			if m := gradleDist.FindStringSubmatch(v); m != nil {
				return m[1]
			}
		}
	}
	return "?"
}

// BazelVersion returns the Bazel version a project pins in .bazelversion, "" if none.
func BazelVersion(root string) string {
	data, err := os.ReadFile(filepath.Join(root, ".bazelversion"))
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(data))
	if v == "" {
		return "?"
	}
	return v
}
