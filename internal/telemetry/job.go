package telemetry

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/binarycat0/buildyard/internal/store"
)

// Labels describe a job for its trace and log records.
type Labels struct {
	Project string // project name
	Task    string // saved task name; empty for a one-off run
	Tool    string // mvn, mvnd, mvnw
	GitHead string // commit the project was at
}

// Job exports one build. Its trace looks like this:
//
//	<project> / <task>             the whole job
//	├─ startup                     process start until Maven knows the reactor (JVM, POMs, extensions)
//	│  └─ download …               downloads before any module started
//	├─ <module>                    one per reactor module
//	│  ├─ <plugin>:<goal> (<id>)   one per goal execution
//	│  └─ download …               downloads made while building that module
//	└─ download …                  downloads outside any module
//
// Every output line becomes a log record that points at the job's root span.
// Feed it the build tool events (Event) and output lines (Line), then call End.
type Job struct {
	e              *Exporter
	traces, logs   bool
	traceID        string
	rootID         string
	startupID      string
	start          time.Time
	name           string
	attrs          []kv // on the root span and every log record
	mu             sync.Mutex
	names          map[string]string // module key → name, from the session event
	modules        map[string]*open  // running modules by key
	mojos          map[string]*open  // running goals by module|mojo|execution
	sessionAt      time.Time
	ended          bool
	moduleCount    int
	modulesFailed  int
	dl             dlStats
	lastLineAtNano int64
	logModule      string // module and goal the output is about, from Maven's own markers
	logGoal        string
	rootAttrs      []kv // only on the root span
}

type open struct {
	id    string
	start time.Time
	attrs []kv
	dl    dlStats
}

type dlStats struct{ count, failed, bytes, ms int64 }

func (d *dlStats) add(ok bool, bytes, ms int64) {
	d.count++
	d.ms += ms
	if ok {
		d.bytes += bytes
	} else {
		d.failed++
	}
}

func (d dlStats) attrs() []kv {
	if d.count == 0 {
		return nil
	}
	return []kv{num("buildyard.downloads.count", d.count), num("buildyard.downloads.failed", d.failed),
		num("buildyard.downloads.bytes", d.bytes), num("buildyard.downloads.ms", d.ms)}
}

// StartJob begins exporting a job that starts now. It returns nil when export is off
// or neither traces nor logs are wanted.
func (e *Exporter) StartJob(j store.Job, l Labels, argv []string) *Job {
	cfg := e.Settings()
	if !cfg.Enabled || (!cfg.Traces && !cfg.Logs) {
		return nil
	}
	name := l.Project
	if name == "" {
		name = "build"
	}
	if l.Task != "" {
		name += " / " + l.Task
	}
	attrs := []kv{num("buildyard.job.id", j.ID), str("buildyard.project", l.Project)}
	if l.Task != "" {
		attrs = append(attrs, str("buildyard.task", l.Task))
	}
	if l.Tool != "" {
		attrs = append(attrs, str("buildyard.tool", l.Tool))
	}
	var rootAttrs []kv
	if len(argv) > 0 {
		rootAttrs = append(rootAttrs, str("process.command_line", strings.Join(argv, " ")))
	}
	if l.GitHead != "" {
		rootAttrs = append(rootAttrs, str("vcs.ref.head.revision", l.GitHead))
	}
	return &Job{e: e, traces: cfg.Traces, logs: cfg.Logs, traceID: newTraceID(), rootID: newSpanID(),
		startupID: newSpanID(), start: time.Now(), name: name, attrs: attrs, rootAttrs: rootAttrs,
		names: map[string]string{}, modules: map[string]*open{}, mojos: map[string]*open{}}
}

// TraceID is the job's trace, as Tempo and Grafana show it.
func (j *Job) TraceID() string { return j.traceID }

// event is one JSON line from the Buildyard Maven extension.
type event struct {
	T        string `json:"t"`
	Key      string `json:"key"`
	Status   string `json:"status"`
	Ms       int64  `json:"ms"`
	Ts       int64  `json:"ts"`
	Mojo     string `json:"mojo"`
	ID       string `json:"id"`
	Plugin   string `json:"plugin"`
	Phase    string `json:"phase"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Repo     string `json:"repo"`
	URL      string `json:"url"`
	OK       bool   `json:"ok"`
	Bytes    int64  `json:"bytes"`
	Projects []struct {
		Key       string `json:"key"`
		Name      string `json:"name"`
		Packaging string `json:"packaging"`
	} `json:"projects"`
}

// Event applies one build tool event line.
func (j *Job) Event(line []byte) {
	if j == nil {
		return // events are followed even without traces: log records use the reactor
	}
	var ev event
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	ts := time.UnixMilli(ev.Ts)
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.ended {
		return
	}
	switch ev.T {
	case "session":
		if !j.sessionAt.IsZero() {
			return // a nested Maven run (a plugin forking Maven) keeps the outer reactor
		}
		j.sessionAt = ts
		for _, p := range ev.Projects {
			j.names[p.Key] = p.Name
		}
		j.moduleCount = len(ev.Projects)
		j.span(span{SpanID: j.startupID, ParentID: j.rootID, Name: "startup", Start: j.start, End: ts,
			Attrs: []kv{num("buildyard.modules", int64(len(ev.Projects)))}})
	case "project_start":
		name, ok := j.names[ev.Key]
		if !ok || j.modules[ev.Key] != nil {
			return
		}
		j.modules[ev.Key] = &open{id: newSpanID(), start: ts,
			attrs: []kv{str("maven.module", ev.Key), str("maven.module.name", name)}}
	case "project_end":
		m := j.modules[ev.Key]
		if m == nil {
			return
		}
		delete(j.modules, ev.Key)
		s := span{SpanID: m.id, ParentID: j.rootID, Name: j.names[ev.Key], Start: m.start, End: ts,
			Attrs: append(append(m.attrs, str("buildyard.status", ev.Status)), m.dl.attrs()...)}
		if ev.Status == "failed" {
			j.modulesFailed++
			s.Error = "module failed"
		}
		j.span(s)
	case "mojo_start":
		k := ev.Key + "|" + ev.Mojo + "|" + ev.ID
		if j.mojos[k] != nil {
			return
		}
		parent := j.rootID
		if m := j.modules[ev.Key]; m != nil {
			parent = m.id
		}
		attrs := []kv{str("maven.module", ev.Key), str("maven.goal", ev.Mojo), str("maven.execution", ev.ID)}
		if ev.Plugin != "" {
			attrs = append(attrs, str("maven.plugin", ev.Plugin))
		}
		if ev.Phase != "" {
			attrs = append(attrs, str("maven.phase", ev.Phase))
		}
		j.mojos[k] = &open{id: parent, start: ts, attrs: attrs} // id holds the parent until it ends
	case "mojo_end":
		k := ev.Key + "|" + ev.Mojo + "|" + ev.ID
		m := j.mojos[k]
		if m == nil {
			return
		}
		delete(j.mojos, k)
		s := span{SpanID: newSpanID(), ParentID: m.id, Name: ev.Mojo + " (" + ev.ID + ")", Start: m.start, End: ts,
			Attrs: append(m.attrs, str("buildyard.status", ev.Status))}
		if ev.Status == "failed" {
			s.Error = "goal failed"
		}
		j.span(s)
	case "download":
		parent := j.rootID
		if m := j.modules[ev.Key]; ev.Key != "" && m != nil {
			parent = m.id
			m.dl.add(ev.OK, ev.Bytes, ev.Ms)
		} else if j.sessionAt.IsZero() || !ts.After(j.sessionAt) {
			parent = j.startupID
		}
		j.dl.add(ev.OK, ev.Bytes, ev.Ms)
		attrs := []kv{str("maven.download.kind", ev.Kind), str("maven.download.name", ev.Name),
			str("maven.repository", ev.Repo), flag("maven.download.found", ev.OK)}
		if ev.URL != "" {
			attrs = append(attrs, str("maven.repository.url", ev.URL))
		}
		if ev.OK {
			attrs = append(attrs, num("maven.download.bytes", ev.Bytes))
		}
		if ev.Key != "" {
			attrs = append(attrs, str("maven.module", ev.Key))
		}
		j.span(span{SpanID: newSpanID(), ParentID: parent, Name: "download " + ev.Name,
			Start: ts.Add(-time.Duration(ev.Ms) * time.Millisecond), End: ts, Attrs: attrs})
	}
}

func (j *Job) span(s span) {
	if !j.traces {
		return
	}
	s.TraceID, s.Kind = j.traceID, 1
	j.e.addSpan(s)
}

// End closes what is still open and exports the job's root span.
func (j *Job) End(status string, exit *int) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.ended {
		return
	}
	j.ended = true
	if !j.traces {
		return
	}
	now := time.Now()
	// Goals and modules still running when the build tool exited were cut short.
	for _, m := range j.mojos {
		j.span(span{SpanID: newSpanID(), ParentID: m.id, Name: goalName(m.attrs), Start: m.start, End: now,
			Attrs: append(m.attrs, str("buildyard.status", "cancelled"))})
	}
	for key, m := range j.modules {
		j.span(span{SpanID: m.id, ParentID: j.rootID, Name: j.names[key], Start: m.start, End: now,
			Attrs: append(append(m.attrs, str("buildyard.status", "cancelled")), m.dl.attrs()...)})
	}
	attrs := append(append(append([]kv{}, j.attrs...), j.rootAttrs...), str("buildyard.status", status))
	if exit != nil {
		attrs = append(attrs, num("process.exit.code", int64(*exit)))
	}
	if j.moduleCount > 0 {
		attrs = append(attrs, num("buildyard.modules", int64(j.moduleCount)), num("buildyard.modules.failed", int64(j.modulesFailed)))
	}
	attrs = append(attrs, j.dl.attrs()...)
	s := span{SpanID: j.rootID, Name: j.name, Start: j.start, End: now, Attrs: attrs}
	switch status {
	case store.JobFailed, store.JobInterrupted:
		s.Error = "build " + status
	}
	j.span(s)
}

func goalName(attrs []kv) string {
	var goal, id string
	for _, a := range attrs {
		if a.Value.S == nil {
			continue
		}
		switch a.Key {
		case "maven.goal":
			goal = *a.Value.S
		case "maven.execution":
			id = *a.Value.S
		}
	}
	return goal + " (" + id + ")"
}
