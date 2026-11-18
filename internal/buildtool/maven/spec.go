package maven

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// RunSpec describes a Maven run. A saved task stores one; a job keeps a snapshot.
type RunSpec struct {
	Kind       string            `json:"kind"`  // "maven"
	Goals      []string          `json:"goals"` // e.g. ["clean", "install"]
	Modules    Modules           `json:"modules"`
	Profiles   []string          `json:"profiles"`   // -P; "!id" deactivates
	Skips      []string          `json:"skips"`      // skip preset ids
	Properties map[string]string `json:"properties"` // -Dkey=value; empty value gives -Dkey
	Flags      Flags             `json:"flags"`
	LogLevel   string            `json:"logLevel"`  // default | warn | quiet | debug
	ExtraArgs  []string          `json:"extraArgs"` // appended as is
}

// Modules selects part of the reactor.
type Modules struct {
	Mode               string   `json:"mode"` // all | selected
	List               []string `json:"list"` // ":artifactId" or "groupId:artifactId"
	AlsoMake           bool     `json:"alsoMake"`
	AlsoMakeDependents bool     `json:"alsoMakeDependents"`
}

// Flags are the common Maven switches.
type Flags struct {
	Offline            bool   `json:"offline"`            // -o
	NoSnapshotUpdates  bool   `json:"noSnapshotUpdates"`  // -nsu
	Threads            string `json:"threads"`            // -T, e.g. "1C", "4"
	NoTransferProgress bool   `json:"noTransferProgress"` // -ntp
	FailMode           string `json:"failMode"`           // "" | fast (-ff) | end (-fae) | never (-fn)
	Errors             bool   `json:"errors"`             // -e
}

// SkipPreset is a named group of arguments that skips part of the build.
type SkipPreset struct {
	ID    string   `json:"id"`
	Label string   `json:"label"`
	Args  []string `json:"args"`
	Note  string   `json:"note,omitempty"`
}

// DefaultSkipPresets are offered for every Maven project; a project can override them
// by id or add its own.
func DefaultSkipPresets() []SkipPreset {
	return []SkipPreset{
		{ID: "tests", Label: "Tests", Args: []string{"-DskipTests"}, Note: "tests are compiled but not run"},
		{ID: "testCompile", Label: "Test compilation", Args: []string{"-Dmaven.test.skip=true"}, Note: "breaks modules that depend on a test-jar"},
		{ID: "checkstyle", Label: "Checkstyle", Args: []string{"-Dcheckstyle.skip"}},
		{ID: "spotless", Label: "Spotless", Args: []string{"-Dspotless.check.skip", "-Dspotless.apply.skip"}},
		{ID: "javadoc", Label: "Javadoc", Args: []string{"-Dmaven.javadoc.skip"}},
		{ID: "sources", Label: "Source jars", Args: []string{"-Dmaven.source.skip"}},
		{ID: "enforcer", Label: "Enforcer", Args: []string{"-Denforcer.skip"}},
		{ID: "license", Label: "License checks", Args: []string{"-Dlicense.skip", "-Drat.skip"}},
	}
}

// MergePresets returns the defaults with the project's presets applied: same id replaces,
// new ids are appended. A project preset with no args removes a default.
func MergePresets(project []SkipPreset) []SkipPreset {
	out := DefaultSkipPresets()
	for _, p := range project {
		i := indexPreset(out, p.ID)
		switch {
		case i >= 0 && len(p.Args) == 0:
			out = append(out[:i], out[i+1:]...)
		case i >= 0:
			out[i] = p
		case len(p.Args) > 0:
			out = append(out, p)
		}
	}
	return out
}

func indexPreset(list []SkipPreset, id string) int {
	for i, p := range list {
		if p.ID == id {
			return i
		}
	}
	return -1
}

var (
	presetID   = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	profileID  = regexp.MustCompile(`^[!?]?[^\s,!?]+$`)
	threadsRe  = regexp.MustCompile(`^\d+(\.\d+)?C?$`)
	propertyRe = regexp.MustCompile(`^[^\s=]+$`)
	moduleRe   = regexp.MustCompile(`^([\w.-]*:)?[\w.-]+$`)
)

// ValidatePresets checks a project's preset list.
func ValidatePresets(list []SkipPreset) error {
	seen := map[string]bool{}
	for _, p := range list {
		if !presetID.MatchString(p.ID) {
			return fmt.Errorf("skip preset id %q: use letters, digits, dot, dash or underscore", p.ID)
		}
		if seen[p.ID] {
			return fmt.Errorf("skip preset %q is listed twice", p.ID)
		}
		seen[p.ID] = true
	}
	return nil
}

// Normalize trims empty entries, fills defaults and drops duplicates.
func (s RunSpec) Normalize() RunSpec {
	s.Kind = "maven"
	s.Goals = clean(s.Goals)
	s.Profiles = clean(s.Profiles)
	s.Skips = uniq(clean(s.Skips))
	s.ExtraArgs = clean(s.ExtraArgs)
	s.Modules.List = uniq(clean(s.Modules.List))
	if s.Modules.Mode != "selected" {
		s.Modules = Modules{Mode: "all", List: []string{}}
	}
	props := map[string]string{}
	for k, v := range s.Properties {
		if k = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(k), "-D")); k != "" {
			props[k] = v
		}
	}
	s.Properties = props
	s.Flags.Threads = strings.TrimSpace(s.Flags.Threads)
	if s.LogLevel == "" {
		s.LogLevel = "default"
	}
	return s
}

// Validate checks that the spec can run with the given presets.
func (s RunSpec) Validate(presets []SkipPreset) error {
	if len(s.Goals) == 0 {
		return errors.New("at least one goal is required, e.g. install")
	}
	for _, g := range s.Goals {
		if strings.HasPrefix(g, "-") {
			return fmt.Errorf("goal %s looks like an option; put options in the extra arguments", g)
		}
	}
	if s.Modules.Mode == "selected" {
		if len(s.Modules.List) == 0 {
			return errors.New("select at least one module, or build all modules")
		}
		for _, m := range s.Modules.List {
			if !moduleRe.MatchString(strings.TrimPrefix(m, "!")) {
				return fmt.Errorf("module %q: use :artifactId or groupId:artifactId", m)
			}
		}
	}
	for _, p := range s.Profiles {
		if !profileID.MatchString(p) {
			return fmt.Errorf("profile %q is not a valid profile id", p)
		}
	}
	for _, id := range s.Skips {
		if indexPreset(presets, id) < 0 {
			return fmt.Errorf("unknown skip preset %q", id)
		}
	}
	for k := range s.Properties {
		if !propertyRe.MatchString(k) {
			return fmt.Errorf("property name %q must not contain spaces or =", k)
		}
	}
	if s.Flags.Threads != "" && !threadsRe.MatchString(s.Flags.Threads) {
		return fmt.Errorf("threads %q: use a number like 4 or a per-core factor like 1C", s.Flags.Threads)
	}
	switch s.Flags.FailMode {
	case "", "fast", "end", "never":
	default:
		return fmt.Errorf("fail mode %q: use fast, end or never", s.Flags.FailMode)
	}
	switch s.LogLevel {
	case "default", "warn", "quiet", "debug":
	default:
		return fmt.Errorf("log level %q: use default, warn, quiet or debug", s.LogLevel)
	}
	return nil
}

// Argv builds the command line in a fixed order (docs/PLAN.md, "Building the command"):
// executable, -B, flags, log level, modules, profiles, skips, properties, goals, extra args.
func (s RunSpec) Argv(executable string, presets []SkipPreset) []string {
	argv := []string{executable, "-B"}
	f := s.Flags
	if f.Offline {
		argv = append(argv, "-o")
	}
	if f.NoSnapshotUpdates {
		argv = append(argv, "-nsu")
	}
	if f.Threads != "" {
		argv = append(argv, "-T", f.Threads)
	}
	if f.NoTransferProgress {
		argv = append(argv, "-ntp")
	}
	switch f.FailMode {
	case "fast":
		argv = append(argv, "-ff")
	case "end":
		argv = append(argv, "-fae")
	case "never":
		argv = append(argv, "-fn")
	}
	if f.Errors {
		argv = append(argv, "-e")
	}
	switch s.LogLevel {
	case "warn":
		// Maven 3.9 logs through slf4j-simple; this keeps WARNING and ERROR lines.
		argv = append(argv, "-Dorg.slf4j.simpleLogger.defaultLogLevel=warn")
	case "quiet":
		argv = append(argv, "-q")
	case "debug":
		argv = append(argv, "-X")
	}
	if s.Modules.Mode == "selected" && len(s.Modules.List) > 0 {
		argv = append(argv, "-pl", strings.Join(s.Modules.List, ","))
		if s.Modules.AlsoMake {
			argv = append(argv, "-am")
		}
		if s.Modules.AlsoMakeDependents {
			argv = append(argv, "-amd")
		}
	}
	argv = append(argv, s.selectionArgs(presets)...)
	argv = append(argv, s.Goals...)
	return append(argv, s.ExtraArgs...)
}

// selectionArgs are -P, skip preset arguments and -D properties: everything that can
// change which modules are in the reactor, so discovery uses them too.
func (s RunSpec) selectionArgs(presets []SkipPreset) []string {
	var out []string
	if len(s.Profiles) > 0 {
		out = append(out, "-P", strings.Join(s.Profiles, ","))
	}
	for _, id := range s.Skips {
		if i := indexPreset(presets, id); i >= 0 {
			out = append(out, presets[i].Args...)
		}
	}
	keys := make([]string, 0, len(s.Properties))
	for k := range s.Properties {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v := s.Properties[k]; v != "" {
			out = append(out, "-D"+k+"="+v)
		} else {
			out = append(out, "-D"+k)
		}
	}
	return out
}

func clean(in []string) []string {
	out := []string{}
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
