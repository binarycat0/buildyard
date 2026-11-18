package jdk

import (
	"bytes"
	"context"
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Runner runs a command and returns its stdout. Injectable for tests.
type Runner func(name string, args ...string) ([]byte, error)

func defaultRunner(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}

// fromManagers asks installed JDK managers what they know, before the path scan:
//   - macOS: /usr/libexec/java_home -X lists every JDK registered with the system;
//   - Homebrew: `brew --prefix` gives the real prefix (it may not be /opt/homebrew),
//     whose opt/ and Cellar/ hold the openjdk formulae;
//   - sdkman, jenv, asdf, mise: their roots from SDKMAN_DIR, JENV_ROOT, ASDF_DATA_DIR,
//     MISE_DATA_DIR when set (their CLIs are shell functions, so the directories are the registry).
func (d Detector) fromManagers() []pattern {
	var ps []pattern
	if d.GOOS == "darwin" {
		if out, err := d.run("/usr/libexec/java_home", "-X"); err == nil {
			for _, home := range parseJavaHomeX(out) {
				ps = append(ps, pattern{home, "macos"})
			}
		}
	}
	if brew := d.findBrew(); brew != "" {
		if out, err := d.run(brew, "--prefix"); err == nil {
			prefix := strings.TrimSpace(string(out))
			home := "libexec"
			if d.GOOS == "darwin" {
				home = "libexec/openjdk.jdk/Contents/Home"
			}
			ps = append(ps,
				pattern{filepath.Join(prefix, "opt/openjdk*", home), "homebrew"},
				pattern{filepath.Join(prefix, "Cellar/openjdk*/*", home), "homebrew"},
			)
		}
	}
	for _, m := range []struct{ env, sub, source string }{
		{"SDKMAN_DIR", "candidates/java/*", "sdkman"},
		{"JENV_ROOT", "versions/*", "jenv"},
		{"ASDF_DATA_DIR", "installs/java/*", "asdf"},
		{"MISE_DATA_DIR", "installs/java/*", "mise"},
	} {
		if dir := d.getenv(m.env); dir != "" {
			ps = append(ps, pattern{filepath.Join(dir, m.sub), m.source})
		}
	}
	return ps
}

func (d Detector) run(name string, args ...string) ([]byte, error) {
	if d.Run != nil {
		return d.Run(name, args...)
	}
	return defaultRunner(name, args...)
}

func (d Detector) getenv(k string) string {
	if d.Getenv != nil {
		return d.Getenv(k)
	}
	return os.Getenv(k)
}

func (d Detector) findBrew() string {
	if d.LookPath != nil {
		return d.LookPath("brew")
	}
	if p, err := exec.LookPath("brew"); err == nil {
		return p
	}
	for _, p := range []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew", "/home/linuxbrew/.linuxbrew/bin/brew"} {
		if p = d.sys(p); isFile(p) {
			return p
		}
	}
	return ""
}

// parseJavaHomeX extracts JVMHomePath values from `java_home -X` plist output.
func parseJavaHomeX(out []byte) []string {
	dec := xml.NewDecoder(bytes.NewReader(out))
	dec.Strict = false
	var homes []string
	var lastKey, cur string
	inKey, inString := false, false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			cur = ""
			inKey, inString = t.Name.Local == "key", t.Name.Local == "string"
		case xml.CharData:
			if inKey || inString {
				cur += string(t)
			}
		case xml.EndElement:
			switch {
			case inKey:
				lastKey = cur
			case inString && lastKey == "JVMHomePath":
				homes = append(homes, strings.TrimSpace(cur))
			}
			inKey, inString = false, false
		}
	}
	return homes
}
