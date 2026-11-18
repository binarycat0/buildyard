package maven

import (
	"bufio"
	"encoding/json"
	"io"
)

// SessionProject is one reactor project from the extension's "session" event.
type SessionProject struct {
	Key        string   `json:"key"`
	Name       string   `json:"name"`
	Packaging  string   `json:"packaging"`
	Dir        string   `json:"dir"`
	Aggregator string   `json:"aggregator"` // project whose <modules> list this one
	Parent     string   `json:"parent"`     // <parent>, when it is in the reactor
	Deps       []string `json:"deps"`       // direct upstream projects in the reactor
}

// Session is the extension's "session" event.
type Session struct {
	Root     string           `json:"root"`
	Projects []SessionProject `json:"projects"`
}

// ExtArgs loads the Buildyard extension and points it at an events file.
func ExtArgs(jar, eventsFile string) []string {
	if jar == "" || eventsFile == "" {
		return nil
	}
	return []string{"-Dmaven.ext.class.path=" + jar, "-Dbuildyard.events=" + eventsFile}
}

// ReadSession returns the first "session" event of an events file, or nil.
func ReadSession(r io.Reader) *Session {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024) // a large reactor is one long line
	for sc.Scan() {
		var head struct {
			T string `json:"t"`
		}
		if json.Unmarshal(sc.Bytes(), &head) != nil || head.T != "session" {
			continue
		}
		var s Session
		if json.Unmarshal(sc.Bytes(), &s) == nil {
			return &s
		}
	}
	return nil
}
