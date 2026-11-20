package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/binarycat0/buildyard/internal/buildtool/maven"
	"github.com/binarycat0/buildyard/internal/runner"
	"github.com/binarycat0/buildyard/internal/store"
)

// tailLines is how many lines a live log stream starts with when the client gives no position.
const tailLines = 2000

// jobView is a job plus what only the runner knows.
type jobView struct {
	store.Job
	LiveLines       int        `json:"liveLines"`       // lines so far while running; logLines once finished
	StopRequestedAt *time.Time `json:"stopRequestedAt"` // set while a running job is being stopped
}

func (s *Server) view(j store.Job) jobView {
	v := jobView{Job: j, LiveLines: j.LogLines}
	if s.Runner != nil {
		if n := s.Runner.Lines(j.ID); n >= 0 {
			v.LiveLines = n
		}
		if t := s.Runner.StopRequested(j.ID); !t.IsZero() {
			v.StopRequestedAt = &t
		}
	}
	return v
}

// preview is what a run would execute.
type preview struct {
	Argv     []string          `json:"argv"`
	Dir      string            `json:"dir"`
	JavaHome string            `json:"javaHome"` // "" = the daemon's environment
	Env      map[string]string `json:"env"`      // project variables added to the daemon's environment
	// MavenOpts is MAVEN_OPTS as the run gets it, when the memory setting changed it.
	MavenOpts string `json:"mavenOpts,omitempty"`
	// ProjectArgs are the arguments the project's task defaults add (marked in the UI).
	ProjectArgs []string `json:"projectArgs"`
	Error       string   `json:"error,omitempty"`
}

// presetsFor returns the default skip presets with the project's own applied.
func presetsFor(p store.Project) []maven.SkipPreset {
	var own []maven.SkipPreset
	_ = json.Unmarshal(p.SkipPresets, &own)
	return maven.MergePresets(own)
}

// taskDefaultsFor returns the project's task defaults, without skips whose preset was
// removed since.
func taskDefaultsFor(p store.Project) maven.TaskDefaults {
	var d maven.TaskDefaults
	_ = json.Unmarshal(p.TaskDefaults, &d)
	presets := presetsFor(p)
	var skips []string
	for _, id := range d.Skips {
		for _, sp := range presets {
			if sp.ID == id {
				skips = append(skips, id)
				break
			}
		}
	}
	d.Skips = skips
	return d.Normalize()
}

// prepare resolves a spec into a command: the task's settings over the project's task
// defaults and resources. A non-empty preview.Error means it cannot run.
func (s *Server) prepare(ctx context.Context, p store.Project, spec maven.RunSpec) (preview, runner.Command, error) {
	defs := taskDefaultsFor(p)
	spec = spec.Normalize().WithTaskDefaults(defs).WithDefaults(resourcesFor(p))
	presets := presetsFor(p)
	pv := preview{Dir: p.RootPath, Env: p.Env, ProjectArgs: nonNil(defs.DefaultArgs(presets))}
	info, err := s.runtimeFor(ctx, p)
	if err != nil {
		return pv, runner.Command{}, err
	}
	if info.JDK != nil {
		pv.JavaHome = info.JDK.Home
	}
	exe := "mvn"
	if info.Executable != nil {
		exe = info.Executable.Path
	}
	pv.Argv = spec.Argv(exe, presets)
	switch {
	case info.ExecutableError != "":
		pv.Error = info.ExecutableError
	case spec.Validate(presets) != nil:
		pv.Error = spec.Validate(presets).Error()
	case info.JDK != nil && info.JDK.Missing:
		pv.Error = "JDK " + info.JDK.Home + " was not found by the last detection"
	}
	env := buildEnv(os.Environ(), p.Env, pv.JavaHome)
	if exe := filepath.Base(exe); exe != "mvnd" {
		if opts, ok := spec.MavenOpts(lookupEnv(env, "MAVEN_OPTS")); ok {
			pv.MavenOpts = opts
			env = append(env, "MAVEN_OPTS="+opts) // later entries win in exec
		}
	}
	cmd := runner.Command{Argv: pv.Argv, Dir: p.RootPath, Env: env}
	cmd.SignalLeaderOnly = info.Executable != nil && info.Executable.Kind == maven.ModeMvnd
	cmd.Parser = func() runner.OutputParser { return &mavenOutput{p: maven.NewLogParser()} }
	if jar := s.ExtJar; jar != "" {
		cmd.EventArgs = func(path string) []string { return maven.ExtArgs(jar, path) }
	}
	return pv, cmd, nil
}

// mavenOutput adapts the Maven log parser to the runner.
type mavenOutput struct{ p *maven.LogParser }

func (o *mavenOutput) Line(n int, text string) ([]store.JobProblem, bool) {
	probs, changed := o.p.Line(n, text)
	var out []store.JobProblem
	for _, p := range probs {
		out = append(out, store.JobProblem{Kind: p.Kind, Module: p.Module, File: p.File, Line: p.Line, Message: p.Message, LogLine: p.LogLine})
	}
	return out, changed
}

func (o *mavenOutput) Modules() []store.JobModule {
	out := make([]store.JobModule, len(o.p.Modules))
	for i, m := range o.p.Modules {
		out[i] = store.JobModule{Key: m.Name, Name: m.Name, Ord: i, Status: m.Status, DurationMs: m.Ms}
	}
	return out
}

// resourcesFor returns the project's default resources.
func resourcesFor(p store.Project) maven.Resources {
	var r maven.Resources
	_ = json.Unmarshal(p.Resources, &r)
	return r
}

func lookupEnv(env []string, key string) string {
	v := ""
	for _, kv := range env {
		if k, val, ok := strings.Cut(kv, "="); ok && k == key {
			v = val
		}
	}
	return v
}

// buildEnv layers the project variables and the chosen JDK over the daemon's environment.
func buildEnv(base []string, project map[string]string, javaHome string) []string {
	env := map[string]string{}
	var order []string
	set := func(k, v string) {
		if _, ok := env[k]; !ok {
			order = append(order, k)
		}
		env[k] = v
	}
	for _, kv := range base {
		if k, v, ok := strings.Cut(kv, "="); ok {
			set(k, v)
		}
	}
	for k, v := range project {
		set(k, v)
	}
	if javaHome != "" {
		set("JAVA_HOME", javaHome)
		path := filepath.Join(javaHome, "bin")
		if old := env["PATH"]; old != "" {
			path += string(os.PathListSeparator) + old
		}
		set("PATH", path)
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+env[k])
	}
	return out
}

type runRequest struct {
	Spec      *maven.RunSpec `json:"spec"`
	ProfileID int64          `json:"profileId"` // run a saved task instead of spec
}

func (s *Server) projectFor(w http.ResponseWriter, r *http.Request) (store.Project, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return store.Project{}, false
	}
	p, err := s.Store.GetProject(r.Context(), id)
	if err != nil {
		s.respondProject(w, p, err)
		return p, false
	}
	return p, true
}

func (s *Server) previewJob(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectFor(w, r)
	if !ok {
		return
	}
	var req runRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	var spec maven.RunSpec
	if req.Spec != nil {
		spec = *req.Spec
	}
	pv, _, err := s.prepare(r.Context(), p, spec)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pv)
}

func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectFor(w, r)
	if !ok {
		return
	}
	var req runRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	var profileID *int64
	var spec maven.RunSpec
	switch {
	case req.ProfileID > 0:
		prof, err := s.Store.GetProfile(r.Context(), req.ProfileID)
		if err != nil || prof.ProjectID != p.ID {
			writeError(w, http.StatusBadRequest, "bad_request", "unknown task for this project")
			return
		}
		if err := json.Unmarshal(prof.Spec, &spec); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "the saved task has an invalid spec")
			return
		}
		profileID = &prof.ID
	case req.Spec != nil:
		spec = *req.Spec
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "give spec or profileId")
		return
	}
	s.start(w, r, p, spec, profileID)
}

func (s *Server) start(w http.ResponseWriter, r *http.Request, p store.Project, spec maven.RunSpec, profileID *int64) {
	pv, cmd, err := s.prepare(r.Context(), p, spec)
	if err != nil {
		s.internal(w, err)
		return
	}
	if pv.Error != "" {
		writeError(w, http.StatusBadRequest, "bad_request", pv.Error)
		return
	}
	specJSON, _ := json.Marshal(spec.Normalize())
	job, err := s.Runner.Enqueue(r.Context(), store.Job{ProjectID: p.ID, ProfileID: profileID, Spec: specJSON}, cmd)
	if errors.Is(err, runner.ErrClosed) {
		writeError(w, http.StatusServiceUnavailable, "unavailable", err.Error())
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.view(job))
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectFor(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	jobs, err := s.Store.ListJobs(r.Context(), p.ID, limit)
	if err != nil {
		s.internal(w, err)
		return
	}
	out := make([]jobView, len(jobs))
	for i, j := range jobs {
		out[i] = s.view(j)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) jobFor(w http.ResponseWriter, r *http.Request) (store.Job, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return store.Job{}, false
	}
	j, err := s.Store.GetJob(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "job not found")
		return j, false
	}
	if err != nil {
		s.internal(w, err)
		return j, false
	}
	return j, true
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	if j, ok := s.jobFor(w, r); ok {
		writeJSON(w, http.StatusOK, s.view(j))
	}
}

// controlJob handles cancel (stop, then kill after a grace period), stop (graceful,
// like Ctrl+C) and kill (immediate). A queued job is dropped by any of them.
func (s *Server) controlJob(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		j, ok := s.jobFor(w, r)
		if !ok {
			return
		}
		do := map[string]func(int64) error{"cancel": s.Runner.Cancel, "stop": s.Runner.Stop, "kill": s.Runner.Kill}[action]
		if err := do(j.ID); errors.Is(err, runner.ErrNotActive) {
			writeError(w, http.StatusConflict, "conflict", "the job is already "+j.Status)
			return
		}
		if fresh, err := s.Store.GetJob(r.Context(), j.ID); err == nil {
			j = fresh
		}
		writeJSON(w, http.StatusAccepted, s.view(j))
	}
}

func (s *Server) rerunJob(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobFor(w, r)
	if !ok {
		return
	}
	p, err := s.Store.GetProject(r.Context(), j.ProjectID)
	if err != nil {
		s.respondProject(w, p, err)
		return
	}
	var spec maven.RunSpec
	if err := json.Unmarshal(j.Spec, &spec); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "this job has no reusable spec")
		return
	}
	// ?without=failed leaves out the modules that failed and everything depending on them.
	if r.URL.Query().Get("without") == "failed" {
		refs, err := s.failedModules(r.Context(), j)
		if err != nil {
			s.internal(w, err)
			return
		}
		if len(refs) == 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "this job has no failed modules recorded")
			return
		}
		spec = spec.Without(refs)
	}
	s.start(w, r, p, spec, j.ProfileID)
}

// jobModules returns per-module progress: live for an active job, stored otherwise.
func (s *Server) jobModules(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobFor(w, r)
	if !ok {
		return
	}
	if s.Runner != nil {
		if mods := s.Runner.Modules(j.ID); mods != nil {
			writeJSON(w, http.StatusOK, mods)
			return
		}
	}
	mods, err := s.Store.ListJobModules(r.Context(), j.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, mods)
}

// jobProblems returns the problems found in a job's log: live for an active job.
func (s *Server) jobProblems(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobFor(w, r)
	if !ok {
		return
	}
	if s.Runner != nil && !j.Finished() {
		if probs := s.Runner.Problems(j.ID); probs != nil {
			writeJSON(w, http.StatusOK, probs)
			return
		}
	}
	probs, err := s.Store.ListJobProblems(r.Context(), j.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, probs)
}

func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

type logPage struct {
	From  int      `json:"from"`
	Lines []string `json:"lines"`
	Total int      `json:"total"`
}

// jobLog returns lines [from, from+limit) of the log file.
func (s *Server) jobLog(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobFor(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	from, _ := strconv.Atoi(q.Get("from"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if from < 1 {
		from = 1
	}
	if limit <= 0 || limit > 50000 {
		limit = 5000
	}
	lines, total, err := runner.ReadLines(j.LogPath, from, limit)
	if err != nil && !os.IsNotExist(err) {
		s.internal(w, err)
		return
	}
	if lines == nil {
		lines = []string{}
	}
	writeJSON(w, http.StatusOK, logPage{From: from, Lines: lines, Total: total})
}

// jobStream streams a job's log as server-sent events: "log" events (id = line number),
// "status" events, and a final "end" event after which the client should close. A client
// reconnecting with Last-Event-ID continues after that line; ?from=N starts at line N;
// otherwise the stream starts with the last 2000 lines.
func (s *Server) jobStream(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobFor(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}
	from := 0
	if last, err := strconv.Atoi(r.Header.Get("Last-Event-ID")); err == nil {
		from = last + 1
	} else if f, err := strconv.Atoi(r.URL.Query().Get("from")); err == nil && f > 0 {
		from = f
	}

	var sub *runner.Sub
	if s.Runner != nil {
		sub = s.Runner.Subscribe(j.ID)
	}
	if sub != nil {
		defer sub.Close()
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(event, id string, data any) {
		b, _ := json.Marshal(data)
		if id != "" {
			fmt.Fprintf(w, "id: %s\n", id)
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	}

	// Catch up from the file: up to sub.From for a live job, everything for a finished one.
	upto := -1
	if sub != nil {
		upto = sub.From
	}
	total := upto
	if total < 0 {
		_, total, _ = runner.ReadLines(j.LogPath, 1, 1)
	}
	if from == 0 {
		from = max(1, total-tailLines+1)
	}
	if from <= total {
		limit := 0
		if upto >= 0 {
			limit = upto - from + 1
		}
		lines, _, _ := runner.ReadLines(j.LogPath, from, limit)
		for i, l := range lines {
			n := from + i
			send("log", strconv.Itoa(n), runner.Event{Type: "line", N: n, Text: l})
		}
	}
	send("status", "", runner.Event{Type: "status", Status: j.Status})
	if sub != nil {
		if sub.Modules != nil {
			send("modules", "", sub.Modules)
		}
		send("problems", "", nonNil(sub.Problems))
	} else {
		if mods, err := s.Store.ListJobModules(r.Context(), j.ID); err == nil && len(mods) > 0 {
			send("modules", "", mods)
		}
		if probs, err := s.Store.ListJobProblems(r.Context(), j.ID); err == nil {
			send("problems", "", probs)
		}
	}
	flusher.Flush()

	if sub != nil {
		ping := time.NewTicker(15 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ping.C:
				fmt.Fprint(w, ": ping\n\n")
				flusher.Flush()
			case ev, ok := <-sub.C:
				if !ok {
					if sub.Dropped {
						return // the client reconnects with Last-Event-ID
					}
					goto end
				}
				switch ev.Type {
				case "line":
					send("log", strconv.Itoa(ev.N), ev)
				case "modules":
					send("modules", "", ev.Modules)
				case "module":
					send("module", "", ev.Module)
				case "problem":
					send("problem", "", ev.Problem)
				default:
					send("status", "", ev)
				}
				if len(sub.C) == 0 {
					flusher.Flush()
				}
			}
		}
	}
end:
	if fin, err := s.Store.GetJob(r.Context(), j.ID); err == nil {
		send("end", "", s.view(fin))
	}
	flusher.Flush()
}
