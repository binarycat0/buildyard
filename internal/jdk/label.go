package jdk

import (
	"bufio"
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Label derives a human-readable name from a JDK home's install location:
//
//	/opt/homebrew/Cellar/openjdk@21/21.0.8/libexec/openjdk.jdk/Contents/Home → "openjdk@21 21.0.8 (Homebrew)"
//	~/.sdkman/candidates/java/21.0.7-tem                                   → "21.0.7-tem (sdkman)"
//	/Library/Java/JavaVirtualMachines/temurin-21.jdk/Contents/Home           → "temurin-21"
//	/usr/lib/jvm/java-21-openjdk-amd64                                       → "java-21-openjdk-amd64"
//
// The home must already be canonical (symlinks resolved), so jenv and opt/ links name their target.
func Label(home string) string {
	parts := strings.Split(filepath.ToSlash(home), "/")
	at := func(name string) int {
		for i := len(parts) - 1; i >= 0; i-- {
			if parts[i] == name {
				return i
			}
		}
		return -1
	}
	after := func(i, n int) string {
		if i >= 0 && i+n < len(parts) {
			return parts[i+n]
		}
		return ""
	}

	if i := at("Cellar"); i >= 0 && after(i, 1) != "" {
		label := after(i, 1)
		if v := after(i, 2); v != "" {
			label += " " + v
		}
		return label + " (Homebrew)"
	}
	for _, m := range []struct{ dir, tool string }{
		{"candidates", "sdkman"}, // ~/.sdkman/candidates/java/<id>
		{"installs", "asdf"},     // ~/.asdf/installs/java/<id> and ~/.local/share/mise/installs/java/<id>
	} {
		if i := at(m.dir); i >= 0 && after(i, 1) == "java" && after(i, 2) != "" {
			tool := m.tool
			if at("mise") >= 0 {
				tool = "mise"
			}
			return after(i, 2) + " (" + tool + ")"
		}
	}
	if i := at("jdks"); i >= 0 && at(".gradle") == i-1 && after(i, 1) != "" {
		return after(i, 1) + " (Gradle)"
	}
	for i := len(parts) - 1; i >= 0; i-- {
		if strings.HasSuffix(parts[i], ".jdk") && parts[i] != "openjdk.jdk" {
			return strings.TrimSuffix(parts[i], ".jdk")
		}
	}
	if n := len(parts); n >= 3 && parts[n-1] == "Home" && parts[n-2] == "Contents" {
		return parts[n-3] // macOS bundle without a .jdk suffix (e.g. IntelliJ downloads)
	}
	return filepath.Base(home)
}

// probe runs `java -XshowSettings:properties -version` and returns the system properties.
// It is a variable so tests can replace it.
var probe = func(java string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, java, "-XshowSettings:properties", "-version").CombinedOutput()
	if err != nil {
		return nil, err
	}
	return parseProperties(out), nil
}

// parseProperties reads "    key = value" lines from -XshowSettings:properties output.
// Multi-line values (indented continuation lines without " = ") are ignored.
func parseProperties(out []byte) map[string]string {
	props := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "    ") {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimSpace(line), " = ")
		if ok {
			props[k] = v
		}
	}
	return props
}
