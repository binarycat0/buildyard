package telemetry

import (
	"regexp"
	"strings"
	"time"
)

// Log records. Every output line is sent as it is, one record per line: the level,
// stack traces and other structure are for the observability stack to parse (see
// doc/observability.md). Buildyard adds only what the stack can't know: the trace,
// the job's attributes, the line number and, when modules build one at a time,
// maven.module and maven.goal from Maven's "--- plugin:goal (id) @ module ---"
// markers. The job's log file is never changed.

var (
	ansi      = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	levelRe   = regexp.MustCompile(`^\[[A-Z]+\] ?`)
	goalRe    = regexp.MustCompile(`^-{3,} (\S+?):\S+:(\S+) \((\S+)\) @ (\S+) -{3,}$`)
	projectRe = regexp.MustCompile(`^-+< (\S+:\S+) >-+$`) // "-----< group:artifact >-----" before "Building …"
)

// Line exports output line n.
func (j *Job) Line(n int, text string) {
	if j == nil || !j.logs {
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
