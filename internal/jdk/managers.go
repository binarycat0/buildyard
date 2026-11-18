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

// Manager is a tool that installs or registers JDKs on this machine.
type Manager struct {
	ID          string `json:"id"`          // matches Info.Source: macos, homebrew, sdkman, jenv, asdf, mise, gradle, system
	Name        string `json:"name"`        // display name
	Kind        string `json:"kind"`        // registry | package manager | version manager | build tool
	Description string `json:"description"` // one line on what it does with JDKs
	Installed   bool   `json:"installed"`
	Location    string `json:"location"` // binary or root directory, "" when not installed
}

// Managers reports which JDK managers exist on this machine. It only checks files and
// runs `brew --prefix`; nothing is installed or changed.
func (d Detector) Managers() []Manager {
	dirOr := func(env, def string) string {
		if v := d.getenv(env); v != "" {
			return v
		}
		return filepath.Join(d.Home, def)
	}
	exists := func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	}
	var out []Manager
	if d.GOOS == "darwin" {
		jh := d.sys("/usr/libexec/java_home")
		out = append(out, Manager{ID: "macos", Name: "macOS java_home", Kind: "registry",
			Description: "lists JDKs installed under /Library/Java/JavaVirtualMachines",
			Installed:   isFile(jh), Location: jh})
	}
	brew := Manager{ID: "homebrew", Name: "Homebrew", Kind: "package manager",
		Description: "installs JDKs as formulae (openjdk, openjdk@21, ...)"}
	if b := d.findBrew(); b != "" {
		brew.Installed, brew.Location = true, b
	}
	out = append(out, brew)
	for _, m := range []struct{ id, name, env, def, desc string }{
		{"sdkman", "SDKMAN!", "SDKMAN_DIR", ".sdkman", "installs and switches JDKs from many vendors (sdk install java ...)"},
		{"jenv", "jenv", "JENV_ROOT", ".jenv", "switches between already installed JDKs; does not install them"},
		{"asdf", "asdf", "ASDF_DATA_DIR", ".asdf", "multi-language version manager with a java plugin"},
		{"mise", "mise", "MISE_DATA_DIR", ".local/share/mise", "multi-language version manager (asdf-compatible)"},
	} {
		root := dirOr(m.env, m.def)
		mg := Manager{ID: m.id, Name: m.name, Kind: "version manager", Description: m.desc}
		if exists(root) {
			mg.Installed, mg.Location = true, root
		}
		out = append(out, mg)
	}
	gradle := Manager{ID: "gradle", Name: "Gradle toolchains", Kind: "build tool",
		Description: "JDKs auto-downloaded by Gradle into ~/.gradle/jdks"}
	if p := filepath.Join(d.Home, ".gradle/jdks"); exists(p) {
		gradle.Installed, gradle.Location = true, p
	}
	out = append(out, gradle)
	if d.GOOS != "darwin" {
		p := d.sys("/usr/lib/jvm")
		out = append(out, Manager{ID: "system", Name: "System packages", Kind: "package manager",
			Description: "JDKs from apt/dnf under /usr/lib/jvm", Installed: exists(p), Location: p})
	}
	return out
}
