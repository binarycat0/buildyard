package telemetry

import (
	"regexp"
	"strings"
	"time"
)

// Log records. With ParseLogs (the default) Maven's output is shaped the way log
// collectors usually shape it (Alloy stage.multiline, OTel Collector recombine):
//
//   - "[ERROR] msg" becomes severity ERROR with body "msg";
//   - lines that continue a record (stack frames, "Caused by:", indented text, an
//     exception header) are appended to it, so a stack trace is one record;
//   - records carry maven.module and maven.goal, taken from Maven's "--- plugin:goal
//     (id) @ module ---" markers, when only one module builds at a time.
//
// Other unprefixed output (forked tests, scripts) stays one record per line. Without
// ParseLogs every line is sent as is. The job's log file is never changed.

const (
	logFlushDelay = 200 * time.Millisecond // a record waits this long for more lines
	maxRecordLen  = 64 * 1024
	maxRecordRows = 1000
)

var (
	ansi       = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	levelRe    = regexp.MustCompile(`^\[(ERROR|FATAL|WARNING|WARN|INFO|DEBUG|TRACE)\] ?`)
	goalRe     = regexp.MustCompile(`^-{3,} (\S+?):\S+:(\S+) \((\S+)\) @ (\S+) -{3,}$`)
	projectRe  = regexp.MustCompile(`^-+< (\S+:\S+) >-+$`) // "-----< group:artifact >-----" before "Building …"
	excRe      = regexp.MustCompile(`^(?:[a-zA-Z_$][\w$]*\.)+[A-Z][\w$]*(?:Exception|Error|Throwable)\b`)
)

var levels = map[string]int{"ERROR": sevError, "FATAL": sevError, "WARNING": sevWarn, "WARN": sevWarn,
	"INFO": sevInfo, "DEBUG": sevDebug, "TRACE": sevDebug}

type pendingLog struct {
	rec   logRecord
	text  strings.Builder
	rows  int
	first int // first line number
	trace bool // an exception header was seen: everything up to the next [LEVEL] line belongs here
	last  time.Time
}

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
	if !j.parseLogs {
		j.emitLocked(logRecord{Body: text, Attrs: j.lineAttrs(n, 1, "", "")})
		return
	}
	if m := levelRe.FindStringSubmatch(text); m != nil {
		j.flushLogLocked()
		body := text[len(m[0]):]
		j.trackLocked(body)
		p := &pendingLog{first: n, rows: 1, last: time.Now()}
		p.rec = logRecord{Severity: levels[m[1]], Time: j.nextTime()}
		p.text.WriteString(body)
		j.pending = p
		j.armLocked()
		return
	}
	if p := j.pending; p != nil && (p.trace || continues(text)) && p.rows < maxRecordRows && p.text.Len()+len(text) < maxRecordLen {
		// An exception's message can span lines, empty ones too (javac errors), before its frames.
		p.trace = p.trace || excRe.MatchString(text)
		p.text.WriteByte('\n')
		p.text.WriteString(text)
		p.rows++
		p.last = time.Now()
		return
	}
	j.flushLogLocked()
	j.emitLocked(logRecord{Body: text, Attrs: j.lineAttrs(n, 1, "", "")})
}

// continues reports whether an unprefixed line belongs to the record before it.
func continues(text string) bool {
	if text == "" {
		return false
	}
	switch text[0] {
	case ' ', '\t':
		return true
	}
	return strings.HasPrefix(text, "Caused by:") || strings.HasPrefix(text, "Suppressed:") ||
		strings.HasPrefix(text, "...") || excRe.MatchString(text)
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

func (j *Job) lineAttrs(first, rows int, module, goal string) []kv {
	attrs := append(make([]kv, 0, len(j.attrs)+4), j.attrs...)
	attrs = append(attrs, num("buildyard.log.line", int64(first)))
	if rows > 1 {
		attrs = append(attrs, num("buildyard.log.lines", int64(rows)))
	}
	// With modules building in parallel the markers interleave: no attribution then.
	if module != "" && len(j.modules) <= 1 {
		attrs = append(attrs, str("maven.module", module))
		if goal != "" {
			attrs = append(attrs, str("maven.goal", goal))
		}
	}
	return attrs
}

// flushLogLocked exports the record being collected, if any.
func (j *Job) flushLogLocked() {
	p := j.pending
	if p == nil {
		return
	}
	j.pending = nil
	p.rec.Body = p.text.String()
	p.rec.Attrs = j.lineAttrs(p.first, p.rows, j.logModule, j.logGoal)
	j.emitLocked(p.rec)
}

func (j *Job) emitLocked(r logRecord) {
	if r.Time.IsZero() {
		r.Time = j.nextTime()
	}
	r.TraceID, r.SpanID = j.traceID, j.rootID
	j.e.addLog(r)
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

// armLocked makes sure a waiting record goes out once no line came for logFlushDelay.
func (j *Job) armLocked() {
	if j.logTimer == nil {
		j.logTimer = time.AfterFunc(logFlushDelay, j.onLogTimer)
		return
	}
	j.logTimer.Reset(logFlushDelay)
}

func (j *Job) onLogTimer() {
	j.mu.Lock()
	defer j.mu.Unlock()
	p := j.pending
	if p == nil || j.ended {
		return
	}
	if wait := logFlushDelay - time.Since(p.last); wait > 0 {
		j.logTimer.Reset(wait)
		return
	}
	j.flushLogLocked()
}
