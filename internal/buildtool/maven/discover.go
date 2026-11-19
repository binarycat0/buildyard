package maven

import (
	"bufio"
	"bytes"
	"regexp"
	"strings"
)

// Module is one reactor project found by discovery.
type Module struct {
	GroupID    string   `json:"groupId"`
	ArtifactID string   `json:"artifactId"`
	Packaging  string   `json:"packaging"`
	Version    string   `json:"version"`
	Name       string   `json:"name"`     // <name>, or the artifactId when the POM has none
	Profiles   []string `json:"profiles"` // profiles active for this module
}

// Key is groupId:artifactId.
func (m Module) Key() string { return m.GroupID + ":" + m.ArtifactID }

// DiscoverArgs returns the arguments (after the executable) that list the reactor:
// help:active-profiles prints every module with its active profiles and runs no
// lifecycle plugins (seconds even for reactors of ~1000 modules). The spec's
// profiles, skips and properties are passed because they can add or remove modules.
// extArgs (see ExtArgs) add module folders and dependencies through the extension.
func DiscoverArgs(spec RunSpec, presets []SkipPreset, offline bool, extArgs []string) []string {
	args := []string{"-B"}
	if offline {
		args = append(args, "-o")
	}
	args = append(args, spec.selectionArgs(presets)...)
	args = append(args, extArgs...)
	return append(args, "help:active-profiles")
}

var (
	// An optional "[thread] " prefix comes from -Dorg.slf4j.simpleLogger.showThreadName=true.
	logPrefix     = regexp.MustCompile(`^(?:\[[^\]]+\] )?\[(?:INFO|WARNING|ERROR|DEBUG)\] ?`)
	activeHeader  = regexp.MustCompile(`^Active Profiles for Project '([^':]+):([^':]+):([^':]+):([^']+)':$`)
	activeProfile = regexp.MustCompile(`^\s+- (\S+) \(source: `)
	buildOrderRow = regexp.MustCompile(`^(.*?)\s+\[([\w.-]+)\]$`)
	buildingLine  = regexp.MustCompile(`^Building (.+) (\S+?)(?:\s+\[\d+/\d+\])?$`)
)

// ParseDiscovery reads help:active-profiles output into modules in reactor order.
// Names come from the "Reactor Build Order" block (same order), or from the
// "Building <name> <version>" line of a single-module project.
func ParseDiscovery(out []byte) []Module {
	var (
		mods      []Module
		names     []string
		inOrder   bool
		single    string
		cur       = -1
		sc        = bufio.NewScanner(bytes.NewReader(out))
		seenOrder bool
	)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		raw := sc.Text()
		msg := logPrefix.ReplaceAllString(raw, "")
		isLog := msg != raw
		switch {
		case isLog && strings.TrimSpace(msg) == "Reactor Build Order:":
			inOrder, seenOrder = true, true
			continue
		case inOrder && isLog:
			if strings.TrimSpace(msg) == "" {
				if len(names) > 0 {
					inOrder = false
				}
				continue
			}
			if m := buildOrderRow.FindStringSubmatch(msg); m != nil {
				names = append(names, strings.TrimSpace(m[1]))
			}
			continue
		case isLog && single == "" && !seenOrder:
			if m := buildingLine.FindStringSubmatch(msg); m != nil {
				single = m[1]
			}
			continue
		}
		if m := activeHeader.FindStringSubmatch(strings.TrimSpace(raw)); m != nil {
			mods = append(mods, Module{GroupID: m[1], ArtifactID: m[2], Packaging: m[3], Version: m[4], Profiles: []string{}})
			cur = len(mods) - 1
			continue
		}
		if cur >= 0 && !isLog {
			if m := activeProfile.FindStringSubmatch(raw); m != nil {
				mods[cur].Profiles = append(mods[cur].Profiles, m[1])
			}
		}
	}
	for i := range mods {
		switch {
		case len(names) == len(mods):
			mods[i].Name = names[i]
		case len(mods) == 1 && single != "":
			mods[i].Name = single
		default:
			mods[i].Name = mods[i].ArtifactID
		}
	}
	return mods
}
