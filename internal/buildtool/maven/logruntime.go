package maven

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// JSON build logs. On the Maven and mvnd versions below, Buildyard wraps the build tool's
// own logger (see maven-log/): the console stays the same, and every log call is also
// written as one JSON line with its level, module, goal and stack trace. Other versions
// run as before, with the raw console output only.

// Logger variants: which wrapper a runtime takes.
const (
	LogMaven3 = "maven3" // Maven 3.9 (SLF4J 1.7, maven-slf4j-provider)
	LogMaven4 = "maven4" // Maven 4 (maven-logging)
	LogMvnd1  = "mvnd1"  // mvnd 1.x daemon (mvnd-logging)
)

// TestedLogVersions are the versions the JSON logger was tried on, by variant: Maven
// versions for maven3/maven4, mvnd versions for mvnd1. The wrappers extend internal
// classes, so only these get it.
var TestedLogVersions = map[string][]string{
	LogMaven3: {"3.9.6", "3.9.9", "3.9.10", "3.9.11"},
	LogMaven4: {"4.0.0-rc-7"},
	LogMvnd1:  {"1.0.2", "1.0.6"},
}

// LogRuntime is an installation the JSON logger supports.
type LogRuntime struct {
	Variant string // LogMaven3 | LogMaven4 | LogMvnd1
	Home    string // Maven home (lib/, conf/), or mvnd home (bin/, mvn/)
	Version string // Maven version, or mvnd version for mvnd
}

// DetectLogRuntime finds the installation an executable runs and whether the JSON logger
// supports it. root is the project (for ./mvnw), userHome the user's home (for the
// wrapper's downloaded distributions). ok is false for anything untested or not found.
func DetectLogRuntime(exe Resolved, root, userHome string) (rt LogRuntime, ok bool) {
	kind := exe.Kind
	if kind == "custom" && filepath.Base(exe.Path) == "mvnd" {
		kind = ModeMvnd
	}
	switch kind {
	case ModeMvnd:
		home := mvndHome(resolve(exe.Path))
		if home == "" {
			return rt, false
		}
		rt = LogRuntime{Variant: LogMvnd1, Home: home, Version: findMvndVersion(filepath.Join(home, "bin", "mvnd"))}
	case ModeWrapper:
		home := wrapperDist(root, userHome)
		if home == "" {
			return rt, false // not downloaded yet: the first run fetches it
		}
		rt = LogRuntime{Home: home, Version: matchJar(filepath.Join(home, "lib"), mavenCoreJar)}
	default: // mvn, or a custom path to a Maven launcher
		home := mavenHome(resolve(exe.Path))
		if home == "" {
			return rt, false
		}
		rt = LogRuntime{Home: home, Version: matchJar(filepath.Join(home, "lib"), mavenCoreJar)}
	}
	if rt.Variant == "" {
		switch {
		case strings.HasPrefix(rt.Version, "3."):
			rt.Variant = LogMaven3
		case strings.HasPrefix(rt.Version, "4."):
			rt.Variant = LogMaven4
		}
	}
	return rt, slices.Contains(TestedLogVersions[rt.Variant], rt.Version)
}

// mavenHome is the Maven installation a launcher belongs to: the nearest directory above
// it with bin/m2.conf and lib/ (Homebrew keeps them under libexec/).
func mavenHome(bin string) string {
	for _, dir := range ancestors(bin) {
		for _, d := range []string{dir, filepath.Join(dir, "libexec")} {
			if isFile(filepath.Join(d, "bin", "m2.conf")) && matchJar(filepath.Join(d, "lib"), mavenCoreJar) != "" {
				return d
			}
		}
	}
	return ""
}

// mvndHome is the mvnd installation a launcher belongs to: bin/mvnd-daemon.conf and mvn/.
func mvndHome(bin string) string {
	for _, dir := range ancestors(bin) {
		for _, d := range []string{dir, filepath.Join(dir, "libexec")} {
			if isFile(filepath.Join(d, "bin", "mvnd-daemon.conf")) && isFile(filepath.Join(d, "mvn", "bin", "m2.conf")) {
				return d
			}
		}
	}
	return ""
}

// wrapperDist is the Maven distribution ./mvnw downloaded for the project, if it did:
// $MAVEN_USER_HOME (default ~/.m2)/wrapper/dists/apache-maven-<v>-bin/<hash>/apache-maven-<v>.
func wrapperDist(root, userHome string) string {
	v := wrapperMavenVersion(root)
	if v == "" {
		return ""
	}
	base := os.Getenv("MAVEN_USER_HOME")
	if base == "" {
		base = filepath.Join(userHome, ".m2")
	}
	matches, _ := filepath.Glob(filepath.Join(base, "wrapper", "dists", "apache-maven-"+v+"*", "*", "apache-maven-"+v))
	for _, m := range matches {
		if isFile(filepath.Join(m, "bin", "m2.conf")) {
			return m
		}
	}
	return ""
}
