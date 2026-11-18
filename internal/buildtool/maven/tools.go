package maven

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// InstalledTool is a globally installed Maven launcher with its version.
type InstalledTool struct {
	ID           string `json:"id"`           // mvn | mvnd
	Name         string `json:"name"`         // display name
	Installed    bool   `json:"installed"`    // found on PATH or in Homebrew/sdkman dirs
	Path         string `json:"path"`         // the launcher, as found
	ResolvedPath string `json:"resolvedPath"` // after following symlinks
	Version      string `json:"version"`      // tool version (Maven version for mvn, mvnd version for mvnd)
	MavenVersion string `json:"mavenVersion"` // Maven bundled with mvnd; same as Version for mvn
	InstallHint  string `json:"installHint"`
}

var (
	mavenCoreJar = regexp.MustCompile(`^maven-core-(\d[\w.\-]*)\.jar$`)
	mvndJar      = regexp.MustCompile(`^mvnd-(?:client|daemon|common)-(\d[\w.\-]*)\.jar$`)
	mvndDir      = regexp.MustCompile(`(?:^|/)(?:maven-)?mvnd[-/](\d+\.\d+[\w.\-]*?)(?:-(?:darwin|linux|windows)[\w\-]*)?(?:/|$)`)
)

// DetectTools finds mvn and mvnd and reads their versions from the installation files,
// without running them (mvnd would start its daemon).
func DetectTools(home string) []InstalledTool {
	mvn := InstalledTool{ID: "mvn", Name: "Maven (mvn)",
		InstallHint: "brew install maven (only needed for projects without ./mvnw)"}
	if p := LookPath("mvn", home); p != "" {
		mvn.Installed, mvn.Path, mvn.ResolvedPath = true, p, resolve(p)
		mvn.Version = findMavenVersion(mvn.ResolvedPath)
		mvn.MavenVersion = mvn.Version
	}
	mvnd := InstalledTool{ID: "mvnd", Name: "Maven Daemon (mvnd)", InstallHint: "brew install mvnd"}
	if p := LookPath("mvnd", home); p != "" {
		mvnd.Installed, mvnd.Path, mvnd.ResolvedPath = true, p, resolve(p)
		mvnd.Version = findMvndVersion(mvnd.ResolvedPath)
		mvnd.MavenVersion = findMavenVersion(mvnd.ResolvedPath)
	}
	return []InstalledTool{mvn, mvnd}
}

func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// ancestors returns the directories above a launcher, nearest first (at most 5).
func ancestors(bin string) []string {
	var out []string
	dir := filepath.Dir(bin)
	for i := 0; i < 5; i++ {
		out = append(out, dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return out
}

// findMavenVersion looks for lib/maven-core-<v>.jar (plain Maven) or mvn/lib/maven-core-<v>.jar (mvnd).
func findMavenVersion(bin string) string {
	for _, dir := range ancestors(bin) {
		for _, lib := range []string{"lib", "mvn/lib", "libexec/lib", "libexec/mvn/lib"} {
			if v := matchJar(filepath.Join(dir, lib), mavenCoreJar); v != "" {
				return v
			}
		}
	}
	return ""
}

func findMvndVersion(bin string) string {
	for _, dir := range ancestors(bin) {
		for _, lib := range []string{"lib", "mvn/lib", "mvn/lib/ext", "libexec/lib", "libexec/mvn/lib/ext"} {
			if v := matchJar(filepath.Join(dir, lib), mvndJar); v != "" {
				return v
			}
		}
	}
	// Fall back to the install directory name: Cellar/mvnd/1.0.2, maven-mvnd-1.0.2-darwin-aarch64, mvnd/1.0.2 (sdkman).
	if m := mvndDir.FindStringSubmatch(filepath.ToSlash(bin)); m != nil {
		return m[1]
	}
	return ""
}

func matchJar(dir string, re *regexp.Regexp) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var versions []string
	for _, e := range entries {
		if m := re.FindStringSubmatch(e.Name()); m != nil {
			versions = append(versions, m[1])
		}
	}
	if len(versions) == 0 {
		return ""
	}
	sort.Strings(versions)
	return strings.TrimSpace(versions[len(versions)-1])
}
