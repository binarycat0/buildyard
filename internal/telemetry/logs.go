package telemetry

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// Log records. On the Maven versions Buildyard's logger supports, each log call arrives
// as a JSON record (Record) and is sent with its severity, module, goal, thread, logger
// and exception: nothing is left for the observability stack to parse. Otherwise every
// output line is sent as it is, one record per line (Line), with the trace, the job's
// attributes, the line number and, when modules build one at a time, maven.module and
// maven.goal from Maven's "--- plugin:goal (id) @ module ---" markers. The job's log
// file is never changed.

var (
	ansi      = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	levelRe   = regexp.MustCompile(`^\[[A-Z]+\] ?`)
	goalRe    = regexp.MustCompile(`^-{3,} (\S+?):\S+:(\S+) \((\S+)\) @ (\S+) -{3,}$`)
	projectRe = regexp.MustCompile(`^-+< (\S+:\S+) >-+$`) // "-----< group:artifact >-----" before "Building …"
)

// Line exports output line n.
func (j *Job) Line(n int, text string) {
	if j == nil || !j.logs || j.structured {
		return
	}
	text = ansi.ReplaceAllString(text, "")
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.ended {
		return
	}
	j.trackLocked(levelRe.ReplaceAllString(text, ""))
	r := logRecord{Body: text, Time: j.nextTime(), TraceID: j.traceID, SpanID: j.rootID}
	r.Attrs = append(make([]kv, 0, len(j.attrs)+3), j.attrs...)
	r.Attrs = append(r.Attrs, num("buildyard.log.line", int64(n)))
	// With modules building in parallel the markers interleave: no attribution then.
	if j.logModule != "" && len(j.modules) <= 1 {
		r.Attrs = append(r.Attrs, str("maven.module", j.logModule))
		if j.logGoal != "" {
			r.Attrs = append(r.Attrs, str("maven.goal", j.logGoal))
		}
	}
	j.e.addLog(r)
}

// trackLocked follows Maven's markers to know which module and goal output is about.
func (j *Job) trackLocked(body string) {
	if m := goalRe.FindStringSubmatch(body); m != nil {
		j.logModule, j.logGoal = j.moduleKey(m[4]), m[1]+":"+m[2]+" ("+m[3]+")"
		return
	}
	if m := projectRe.FindStringSubmatch(body); m != nil {
		j.logModule, j.logGoal = m[1], ""
		return
	}
	// The end of the build is about no module in particular.
	if strings.HasPrefix(body, "Reactor Summary") || strings.HasPrefix(body, "BUILD SUCCESS") || strings.HasPrefix(body, "BUILD FAILURE") {
		j.logModule, j.logGoal = "", ""
	}
}

// moduleKey turns an artifactId from the log into the module key the spans use.
func (j *Job) moduleKey(artifactID string) string {
	for k := range j.names {
		if strings.HasSuffix(k, ":"+artifactID) {
			return k
		}
	}
	return artifactID
}

// jsonRecord is one line of Buildyard's Maven logger (maven-log/).
type jsonRecord struct {
	Ts        int64             `json:"ts"` // ms
	Level     string            `json:"level"`
	Logger    string            `json:"logger"`
	Thread    string            `json:"thread"`
	Msg       string            `json:"msg"`
	MDC       map[string]string `json:"mdc"`
	Exception *struct {
		Type       string `json:"type"`
		Message    string `json:"message"`
		Stacktrace string `json:"stacktrace"`
	} `json:"exception"`
}

// OTLP severity numbers.
var severities = map[string]int{"TRACE": 1, "DEBUG": 5, "INFO": 9, "WARN": 13, "ERROR": 17}

// Record exports one JSON log record.
func (j *Job) Record(line []byte) {
	if j == nil || !j.logs {
		return
	}
	var rec jsonRecord
	if json.Unmarshal(line, &rec) != nil {
		return
	}
	if rec.Logger == "stderr" && strings.HasPrefix(rec.Msg, "SLF4J: Actual binding is of type [") {
		return // SLF4J naming Buildyard's own logger at startup
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.ended {
		return
	}
	r := logRecord{Body: ansi.ReplaceAllString(rec.Msg, ""), Time: j.timeAt(rec.Ts), Severity: severities[rec.Level],
		TraceID: j.traceID, SpanID: j.rootID}
	r.Attrs = append(make([]kv, 0, len(j.attrs)+8), j.attrs...)
	if m := j.recordModule(rec.MDC); m != "" {
		r.Attrs = append(r.Attrs, str("maven.module", m))
	}
	if g := recordGoal(rec.MDC); g != "" {
		r.Attrs = append(r.Attrs, str("maven.goal", g))
	}
	if rec.Logger != "" {
		r.Attrs = append(r.Attrs, str("logger.name", rec.Logger))
	}
	if rec.Thread != "" {
		r.Attrs = append(r.Attrs, str("thread.name", rec.Thread))
	}
	if x := rec.Exception; x != nil {
		r.Attrs = append(r.Attrs, str("exception.type", x.Type))
		if x.Message != "" {
			r.Attrs = append(r.Attrs, str("exception.message", x.Message))
		}
		r.Attrs = append(r.Attrs, str("exception.stacktrace", x.Stacktrace))
	}
	j.e.addLog(r)
}

// recordModule is the module key a record is about: Buildyard's extension sets it on
// every runtime; Maven 4 and mvnd also set the artifactId alone.
func (j *Job) recordModule(mdc map[string]string) string {
	if m := mdc["buildyard.module"]; m != "" {
		return m
	}
	if id := mdc["maven.project.id"]; id != "" {
		return j.moduleKey(id)
	}
	return ""
}

// recordGoal names the goal like the spans do: "compiler:compile (default-compile)".
// Maven 4 writes "compiler:compile@default-compile".
func recordGoal(mdc map[string]string) string {
	if g := mdc["buildyard.goal"]; g != "" {
		return g
	}
	if g, id, ok := strings.Cut(mdc["maven.mojo.id"], "@"); ok {
		return g + " (" + id + ")"
	}
	return ""
}

// timeAt is a record's time made strictly increasing, like nextTime.
func (j *Job) timeAt(ms int64) time.Time {
	now := ms * int64(time.Millisecond)
	if ms == 0 {
		now = time.Now().UnixNano()
	}
	if now <= j.lastLineAtNano {
		now = j.lastLineAtNano + 1
	}
	j.lastLineAtNano = now
	return time.Unix(0, now)
}

// nextTime is now, made strictly increasing: Loki keeps the order of records only
// when their times differ.
func (j *Job) nextTime() time.Time {
	now := time.Now().UnixNano()
	if now <= j.lastLineAtNano {
		now = j.lastLineAtNano + 1
	}
	j.lastLineAtNano = now
	return time.Unix(0, now)
}
