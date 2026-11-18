package maven

import (
	"errors"
	"strings"
)

// RunSpec describes a Maven run. Phase 2 has goals and extra arguments; modules,
// skip presets, flags, profiles and properties come with phase 3 (see docs/PLAN.md).
type RunSpec struct {
	Kind  string   `json:"kind"`      // "maven"
	Goals []string `json:"goals"`     // e.g. ["clean", "install"]
	Args  []string `json:"extraArgs"` // extra arguments, e.g. ["-DskipTests", "-pl", ":core"]
}

// Normalize trims empty entries and fills the kind.
func (s RunSpec) Normalize() RunSpec {
	s.Kind = "maven"
	s.Goals = clean(s.Goals)
	s.Args = clean(s.Args)
	return s
}

// Validate checks that the spec can run.
func (s RunSpec) Validate() error {
	if len(s.Goals) == 0 {
		return errors.New("at least one goal is required, e.g. install")
	}
	for _, g := range s.Goals {
		if strings.HasPrefix(g, "-") {
			return errors.New("goal " + g + " looks like an option; put options in the arguments")
		}
	}
	return nil
}

// Argv builds the command line: executable, -B (no colours or prompts), goals, args.
func (s RunSpec) Argv(executable string) []string {
	argv := []string{executable, "-B"}
	argv = append(argv, s.Goals...)
	return append(argv, s.Args...)
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
