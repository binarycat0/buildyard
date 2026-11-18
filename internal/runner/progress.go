package runner

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/binarycat0/buildyard/internal/store"
)

// progress tracks per-module state from the build tool extension's JSON Lines events:
// session (the reactor), project_start, project_end.
type progress struct {
	mods  []store.JobModule
	index map[string]int
}

type evLine struct {
	T        string `json:"t"`
	Key      string `json:"key"`
	Status   string `json:"status"`
	Ms       int64  `json:"ms"`
	Ts       int64  `json:"ts"`
	Projects []struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	} `json:"projects"`
}

// apply reads one event line. It returns the modules that changed, and whether the
// whole list was (re)built by a session event.
func (p *progress) apply(line []byte) (changed []store.JobModule, session bool) {
	var e evLine
	if json.Unmarshal(line, &e) != nil {
		return nil, false
	}
	switch e.T {
	case "session":
		if len(p.mods) > 0 {
			return nil, false // a nested Maven run (e.g. a plugin forking Maven) keeps the outer reactor
		}
		p.index = map[string]int{}
		for i, pr := range e.Projects {
			p.index[pr.Key] = i
			p.mods = append(p.mods, store.JobModule{Key: pr.Key, Name: pr.Name, Ord: i, Status: "pending"})
		}
		return nil, true
	case "project_start", "project_end":
		i, ok := p.index[e.Key]
		if !ok {
			return nil, false
		}
		m := &p.mods[i]
		if e.T == "project_start" {
			t := time.UnixMilli(e.Ts)
			m.Status, m.StartedAt = "running", &t
		} else {
			m.Status, m.DurationMs = e.Status, e.Ms
		}
		return []store.JobModule{*m}, false
	}
	return nil, false
}

func (p *progress) snapshot() []store.JobModule {
	if len(p.mods) == 0 {
		return nil
	}
	return append([]store.JobModule(nil), p.mods...)
}

// finish closes modules still running when the job ended (killed, stopped, crashed):
// they count as failed, or cancelled when the user stopped the job. It returns them.
func (p *progress) finish(jobStatus string) []store.JobModule {
	var changed []store.JobModule
	for i := range p.mods {
		m := &p.mods[i]
		if m.Status != "running" {
			continue
		}
		m.Status = "failed"
		if jobStatus == store.JobCancelled || jobStatus == store.JobInterrupted {
			m.Status = "cancelled"
		}
		if m.StartedAt != nil {
			m.DurationMs = time.Since(*m.StartedAt).Milliseconds()
		}
		changed = append(changed, *m)
	}
	return changed
}

// applyLocked applies one event line and publishes what changed. rn.mu must be held.
func (rn *run) applyLocked(line []byte) {
	changed, session := rn.prog.apply(line)
	if session {
		rn.publishLocked(Event{Type: "modules", Modules: rn.prog.snapshot()})
	}
	for i := range changed {
		rn.publishLocked(Event{Type: "module", Module: &changed[i]})
	}
}

// finishProgress settles modules left running and returns the final list (nil when the
// job reported none).
func (rn *run) finishProgress(jobStatus string) []store.JobModule {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	for _, m := range rn.prog.finish(jobStatus) {
		rn.publishLocked(Event{Type: "module", Module: &m})
	}
	return rn.prog.snapshot()
}

// eventTail follows a JSON Lines events file the build tool appends to.
type eventTail struct {
	path    string
	f       *os.File
	partial []byte
}

const tailEvery = 250 * time.Millisecond

// follow reads new events until stop is closed, then drains what is left.
func (t *eventTail) follow(rn *run, stop <-chan struct{}) {
	tick := time.NewTicker(tailEvery)
	defer tick.Stop()
	defer func() {
		if t.f != nil {
			t.f.Close()
		}
	}()
	for {
		select {
		case <-stop:
			t.read(rn)
			return
		case <-tick.C:
			t.read(rn)
		}
	}
}

func (t *eventTail) read(rn *run) {
	if t.f == nil {
		f, err := os.Open(t.path)
		if err != nil {
			return // not created yet
		}
		t.f = f
	}
	data, err := io.ReadAll(bufio.NewReader(t.f))
	if err != nil || len(data) == 0 {
		return
	}
	data = append(t.partial, data...)
	last := bytes.LastIndexByte(data, '\n')
	if last < 0 {
		t.partial = data
		return
	}
	t.partial = append([]byte(nil), data[last+1:]...)
	rn.mu.Lock()
	defer rn.mu.Unlock()
	for _, line := range bytes.Split(data[:last], []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) > 0 {
			rn.applyLocked(line)
		}
	}
}
