package maven

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// TaskDefaults are a project's own settings that every task of it gets: the project
// layer between the user's global Maven settings and a task's flags. A task adds to
// them and wins where both set the same thing.
type TaskDefaults struct {
	Profiles   []string          `json:"profiles"`   // -P; a task's "!id" turns one off
	Properties map[string]string `json:"properties"` // -D; a task's value for the same key wins
	Skips      []string          `json:"skips"`      // skip preset ids ticked in every task
}

// Normalize trims empty entries and drops duplicates.
func (d TaskDefaults) Normalize() TaskDefaults {
	s := RunSpec{Profiles: d.Profiles, Properties: d.Properties, Skips: d.Skips}.Normalize()
	return TaskDefaults{Profiles: uniq(s.Profiles), Properties: s.Properties, Skips: s.Skips}
}

// Validate checks the defaults against the project's skip presets.
func (d TaskDefaults) Validate(presets []SkipPreset) error {
	s := RunSpec{Goals: []string{"validate"}, Profiles: d.Profiles, Properties: d.Properties, Skips: d.Skips}.Normalize()
	if err := s.Validate(presets); err != nil {
		return fmt.Errorf("task defaults: %w", err)
	}
	return nil
}

// Empty reports whether nothing is set.
func (d TaskDefaults) Empty() bool {
	return len(d.Profiles) == 0 && len(d.Properties) == 0 && len(d.Skips) == 0
}

// WithTaskDefaults merges the project's task defaults under the spec: project profiles
// first (a task "!id" drops the project's "id"), then the task's; properties with the
// task's values winning; skips from both. Applying it twice changes nothing.
func (s RunSpec) WithTaskDefaults(d TaskDefaults) RunSpec {
	d = d.Normalize()
	if d.Empty() {
		return s
	}
	off := map[string]bool{}
	for _, p := range s.Profiles {
		if strings.HasPrefix(p, "!") {
			off[p[1:]] = true
		}
	}
	var profiles []string
	for _, p := range d.Profiles {
		if !off[strings.TrimPrefix(p, "?")] {
			profiles = append(profiles, p)
		}
	}
	s.Profiles = uniq(append(profiles, s.Profiles...))
	props := map[string]string{}
	for k, v := range d.Properties {
		props[k] = v
	}
	for k, v := range s.Properties {
		props[k] = v
	}
	s.Properties = props
	s.Skips = uniq(append(append([]string{}, d.Skips...), s.Skips...))
	return s
}

// DefaultArgs are the arguments the defaults add, in the order Argv writes them; the UI
// marks these in a task's command as coming from the project.
func (d TaskDefaults) DefaultArgs(presets []SkipPreset) []string {
	d = d.Normalize()
	if d.Empty() {
		return nil
	}
	return RunSpec{Profiles: d.Profiles, Properties: d.Properties, Skips: d.Skips}.selectionArgs(presets)
}

// RepoConfig is the Maven configuration a project keeps in its own repository, read by
// every Maven run in it (also outside Buildyard): .mvn/maven.config and .mvn/jvm.config.
type RepoConfig struct {
	MavenConfig     []string `json:"mavenConfig"`     // arguments added to every run, one per line (Maven 3.9+)
	MavenConfigPath string   `json:"mavenConfigPath"` // "" when the file is missing
	JvmConfig       []string `json:"jvmConfig"`       // JVM options of Maven itself
	JvmConfigPath   string   `json:"jvmConfigPath"`
	Extensions      []string `json:"extensions"` // groupId:artifactId from .mvn/extensions.xml
	ExtensionsPath  string   `json:"extensionsPath"`
}

// ReadRepoConfig reads the .mvn directory of a project root. Missing files are left empty.
func ReadRepoConfig(root string) RepoConfig {
	var c RepoConfig
	if p := filepath.Join(root, ".mvn", "maven.config"); isFile(p) {
		c.MavenConfigPath, c.MavenConfig = p, configLines(p)
	}
	if p := filepath.Join(root, ".mvn", "jvm.config"); isFile(p) {
		c.JvmConfigPath, c.JvmConfig = p, configLines(p)
	}
	if p := filepath.Join(root, ".mvn", "extensions.xml"); isFile(p) {
		c.ExtensionsPath, c.Extensions = p, extensionIDs(p)
	}
	return c
}

// configLines returns the non-empty, non-comment lines of a .mvn config file.
func configLines(path string) []string {
	out := []string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

// extensionIDs pulls groupId:artifactId pairs out of .mvn/extensions.xml without a full
// XML model: the file is small and flat.
func extensionIDs(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{}
	}
	out := []string{}
	text := string(data)
	for {
		i := strings.Index(text, "<extension>")
		if i < 0 {
			break
		}
		text = text[i+len("<extension>"):]
		end := strings.Index(text, "</extension>")
		if end < 0 {
			break
		}
		block := text[:end]
		g, a := tag(block, "groupId"), tag(block, "artifactId")
		if a != "" {
			out = append(out, g+":"+a)
		}
		text = text[end:]
	}
	sort.Strings(out)
	return out
}

func tag(s, name string) string {
	_, rest, ok := strings.Cut(s, "<"+name+">")
	if !ok {
		return ""
	}
	v, _, _ := strings.Cut(rest, "</"+name+">")
	return strings.TrimSpace(v)
}
