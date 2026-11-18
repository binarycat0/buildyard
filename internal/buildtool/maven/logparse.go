package maven

import (
	"regexp"
	"strconv"
	"strings"
)

// Problem is something that went wrong in a build, found in its log.
type Problem struct {
	Kind    string `json:"kind"`    // compile | test | goal | dependency
	Module  string `json:"module"`  // artifactId, when known
	File    string `json:"file"`    // source file (compile)
	Line    int    `json:"line"`    // line in the file (compile) or test source (test)
	Col     int    `json:"col"`     // column (compile)
	Message string `json:"message"` // one line
	LogLine int    `json:"logLine"` // 1-based line in the job log
}

// LogModule is a module as the log shows it: the fallback when the extension sent no
// events. Name is the project name Maven prints (or the artifactId when it has none).
type LogModule struct {
	Name   string
	Status string // pending | running | succeeded | failed | skipped
	Ms     int64
}

// LogParser reads a Maven log one line at a time. It finds problems (compile errors,
// failed tests, failed goals) and, as a fallback, module boundaries from "Reactor Build
// Order", "Building <name> <version> [n/m]" and the "Reactor Summary". A build failure is
// never decided from a bare [ERROR] line: some plugins log [ERROR] and recover.
type LogParser struct {
	// Modules in reactor order; nil until the "Reactor Build Order" block was seen.
	Modules []LogModule
	index   map[string]int

	section     string            // "" | order | summary | tests
	threadMod   map[string]string // thread -> artifactId of its current "@ module"
	threadName  map[string]string // thread -> name of the module it builds
	lastProject string            // module of the last "Failed to execute goal" (for the javac repeat)
	seen        map[string]bool   // problem dedup
}

// NewLogParser returns a parser for one build log.
func NewLogParser() *LogParser {
	return &LogParser{index: map[string]int{}, threadMod: map[string]string{}, threadName: map[string]string{}, seen: map[string]bool{}}
}

var (
	threadRe   = regexp.MustCompile(`^\[([\w.:-]+)\] `) // [main] / [mvn-builder-x] (showThreadName)
	levelRe    = regexp.MustCompile(`^\[(INFO|WARNING|WARN|ERROR|DEBUG)\] ?`)
	buildingRe = regexp.MustCompile(`^Building (.+?) \S+\s+\[(\d+)/(\d+)\]$`)
	mojoRe     = regexp.MustCompile(`^--- .* @ ([\w.-]+) ---$`)
	orderRe    = regexp.MustCompile(`^(.+?)\s+\[[\w-]+\]$`)
	summaryRe  = regexp.MustCompile(`^(.+?) \.+ ?(SUCCESS|FAILURE|SKIPPED)(?: \[\s*([\d:.]+) (s|min|h)\])?`)
	javacRe    = regexp.MustCompile(`^((?:/|[A-Za-z]:\\)\S+?\.\w+):\[(\d+),(\d+)\] (.+)$`)
	goalRe     = regexp.MustCompile(`^Failed to execute goal (?:(\S+) (\([^)]*\) ))?on project ([\w.-]+): (.+)$`)
	testHdrRe  = regexp.MustCompile(`^(Failures|Errors|Failed tests|Tests in error):\s*$`)
	testItemRe = regexp.MustCompile(`^\s{2,}(?:Run \d+: )?([\w.$]+?)(?::(\d+))?(?:\s+»\s+|\s+|$)(.*)$`)
)

// Line parses log line n (1-based). It returns new problems and whether the module list
// changed (Modules was rebuilt or one module's state moved).
func (p *LogParser) Line(n int, text string) (problems []Problem, modulesChanged bool) {
	text = strings.TrimRight(text, " \r")
	thread := ""
	if m := threadRe.FindStringSubmatch(text); m != nil && !levelRe.MatchString(text) {
		thread, text = m[1], text[len(m[0]):]
	}
	level := ""
	if m := levelRe.FindStringSubmatch(text); m != nil {
		level, text = m[1], text[len(m[0]):]
	}
	body := strings.TrimSpace(text)

	// Sections that span lines.
	switch p.section {
	case "order":
		if m := orderRe.FindStringSubmatch(body); m != nil {
			name := strings.TrimSpace(m[1])
			if _, dup := p.index[name]; !dup {
				p.index[name] = len(p.Modules)
				p.Modules = append(p.Modules, LogModule{Name: name, Status: "pending"})
			}
			return nil, true
		}
		if body != "" {
			p.section = ""
		} else if len(p.Modules) > 0 {
			p.section = ""
		}
	case "summary":
		if m := summaryRe.FindStringSubmatch(body); m != nil {
			return nil, p.setStatus(m[1], map[string]string{"SUCCESS": "succeeded", "FAILURE": "failed", "SKIPPED": "skipped"}[m[2]], parseDuration(m[3], m[4]))
		}
		if body != "" {
			p.section = ""
		}
	case "tests":
		if level == "ERROR" || level == "" {
			if m := testItemRe.FindStringSubmatch(text); m != nil && strings.HasPrefix(text, "  ") {
				line, _ := strconv.Atoi(m[2])
				msg := strings.TrimSpace(m[1] + " " + m[3])
				if m[2] != "" {
					msg = strings.TrimSpace(m[1] + ":" + m[2] + " " + m[3])
				}
				return p.add(Problem{Kind: "test", Module: p.moduleOf(thread), Line: line, Message: msg, LogLine: n}), false
			}
		}
		p.section = ""
	}

	switch {
	case body == "Reactor Build Order:":
		p.section = "order"
		p.Modules, p.index = nil, map[string]int{}
		return nil, true
	case strings.HasPrefix(body, "Reactor Summary"):
		p.section = "summary"
		return nil, false
	case testHdrRe.MatchString(body) && (level == "ERROR" || level == ""):
		p.section = "tests"
		return nil, false
	}
	if m := mojoRe.FindStringSubmatch(body); m != nil {
		p.threadMod[thread] = m[1]
		return nil, false
	}
	if m := buildingRe.FindStringSubmatch(body); m != nil {
		return nil, p.start(thread, strings.TrimSpace(m[1]))
	}
	if level != "ERROR" {
		return nil, false
	}
	if m := goalRe.FindStringSubmatch(body); m != nil {
		p.lastProject = m[3]
		kind, msg := "goal", m[4]
		if m[1] == "" {
			kind = "dependency"
		} else {
			msg = shortGoal(m[1]) + " " + m[2] + msg
		}
		return p.add(Problem{Kind: kind, Module: m[3], Message: clip(msg), LogLine: n}), false
	}
	if m := javacRe.FindStringSubmatch(body); m != nil {
		line, _ := strconv.Atoi(m[2])
		col, _ := strconv.Atoi(m[3])
		mod := p.moduleOf(thread)
		if thread == "" || thread == "main" {
			if mod == "" {
				mod = p.lastProject
			}
		}
		return p.add(Problem{Kind: "compile", Module: mod, File: m[1], Line: line, Col: col, Message: clip(m[4]), LogLine: n}), false
	}
	return nil, false
}

func (p *LogParser) moduleOf(thread string) string {
	if m := p.threadMod[thread]; m != "" {
		return m
	}
	if strings.HasPrefix(thread, "mvn-builder-") {
		return strings.TrimPrefix(thread, "mvn-builder-")
	}
	return ""
}

func (p *LogParser) add(pr Problem) []Problem {
	key := pr.Kind + "\x00" + pr.File + "\x00" + strconv.Itoa(pr.Line) + "\x00" + strconv.Itoa(pr.Col) + "\x00" + pr.Message
	if pr.Kind == "goal" || pr.Kind == "dependency" {
		key = pr.Kind + "\x00" + pr.Module
	}
	if p.seen[key] {
		return nil
	}
	p.seen[key] = true
	return []Problem{pr}
}

// start marks a module running. Without -T the previous module of the same thread is
// done; with -T each builder thread runs one module at a time, so the same rule holds.
func (p *LogParser) start(thread, name string) bool {
	changed := false
	if prev := p.threadName[thread]; prev != "" && prev != name {
		changed = p.setStatus(prev, "succeeded", -1) || changed
	}
	p.threadName[thread] = name
	p.threadMod[thread] = ""
	if i, ok := p.index[name]; ok && p.Modules[i].Status == "pending" {
		p.Modules[i].Status = "running"
		changed = true
	}
	return changed
}

// setStatus moves a module to a final state; ms < 0 keeps the duration unknown. A module
// that already has a final state from the Reactor Summary is not overwritten by a guess.
func (p *LogParser) setStatus(name, status string, ms int64) bool {
	i, ok := p.index[strings.TrimSpace(name)]
	if !ok {
		return false
	}
	m := &p.Modules[i]
	if ms < 0 && m.Status != "running" {
		return false
	}
	m.Status = status
	if ms >= 0 {
		m.Ms = ms
	}
	return true
}

// Running returns the names of modules being built now.
func (p *LogParser) Running() []string {
	var out []string
	for _, m := range p.Modules {
		if m.Status == "running" {
			out = append(out, m.Name)
		}
	}
	return out
}

func parseDuration(v, unit string) int64 {
	if v == "" {
		return 0
	}
	parts := strings.Split(v, ":")
	var total float64
	for _, part := range parts {
		f, _ := strconv.ParseFloat(part, 64)
		total = total*60 + f
	}
	switch unit {
	case "min":
		if len(parts) == 1 {
			total *= 60
		}
	case "h":
		if len(parts) == 1 {
			total *= 3600
		} else {
			total *= 60
		}
	}
	return int64(total * 1000)
}

// shortGoal turns "org.apache.maven.plugins:maven-compiler-plugin:3.14.1:compile" into
// "maven-compiler-plugin:compile".
func shortGoal(g string) string {
	parts := strings.Split(g, ":")
	if len(parts) >= 4 {
		return parts[1] + ":" + parts[len(parts)-1]
	}
	return g
}

func clip(s string) string {
	const max = 600
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
