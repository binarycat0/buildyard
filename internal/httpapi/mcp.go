package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/binarycat0/buildyard/internal/buildtool/maven"
	"github.com/binarycat0/buildyard/internal/project"
	"github.com/binarycat0/buildyard/internal/runner"
	"github.com/binarycat0/buildyard/internal/store"
)

// MCP: the Model Context Protocol over streamable HTTP at /mcp, so coding agents can
// run saved tasks and read results. Every request gets a plain JSON reply (no SSE
// stream, no sessions). Settings are read-only here.

const mcpProtocol = "2025-06-18"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	run         func(*Server, context.Context, json.RawMessage) (any, error)
}

func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	var req rpcRequest
	if err := decode(r, &req); err != nil || req.JSONRPC != "2.0" || req.Method == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"jsonrpc": "2.0", "id": nil,
			"error": rpcError{-32700, "expected one JSON-RPC 2.0 request"}})
		return
	}
	if len(req.ID) == 0 { // a notification, e.g. notifications/initialized
		w.WriteHeader(http.StatusAccepted)
		return
	}
	result, rerr := s.mcpDispatch(r.Context(), req)
	resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	if rerr != nil {
		resp["error"] = rerr
	} else {
		resp["result"] = result
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) mcpDispatch(ctx context.Context, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := mcpProtocol
		if p.ProtocolVersion == "2025-03-26" || p.ProtocolVersion == "2024-11-05" {
			version = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "buildyard", "version": Version},
			"instructions": "Buildyard runs saved build tasks (Maven) of local projects. Call list_projects, " +
				"then run_task and get_job with wait_seconds to follow it. Settings are read-only.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": mcpTools}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "invalid params"}
		}
		for _, t := range mcpTools {
			if t.Name == p.Name {
				return toolResult(t.run(s, ctx, p.Arguments)), nil
			}
		}
		return nil, &rpcError{-32602, "unknown tool " + p.Name}
	}
	return nil, &rpcError{-32601, "method not found: " + req.Method}
}

// toolResult wraps a tool's output; errors go back to the agent as text it can act on.
func toolResult(v any, err error) map[string]any {
	if err != nil {
		return map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": err.Error()}}}
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // agents read XML config files and log lines as they are
	enc.SetIndent("", " ")
	enc.Encode(v)
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": b.String()}}}
}

func schema(required []string, props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": nonNil(required)}
}

func prop(typ, desc string) map[string]any { return map[string]any{"type": typ, "description": desc} }

var mcpTools = []mcpTool{
	{Name: "list_projects", Description: "Projects with their directory, git branch, git worktrees and saved tasks (id, name, command, last job).",
		InputSchema: schema(nil, map[string]any{}), run: (*Server).mcpListProjects},
	{Name: "run_task", Description: "Queue a saved task of a project and return the job. Optional overrides change this run only, e.g. " +
		"modules [\":core\"] with properties {\"test\": \"FooTest\"} and dont_skip [\"tests\"] to run one test class.",
		InputSchema: schema([]string{"project", "task"}, map[string]any{
			"project":    prop("string", "project id or name"),
			"task":       prop("string", "task id or name"),
			"worktree":   prop("string", "directory of one of the project's git worktrees (from list_projects); default the project directory"),
			"modules":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "build only these modules (-pl), e.g. \":core\" or \"group:artifact\""},
			"also_make":  prop("boolean", "with modules: also build what they depend on (-am)"),
			"properties": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "extra -Dkey=value, e.g. {\"test\": \"FooTest#bar\"}"},
			"extra_args": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "arguments appended as is"},
			"skip":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "skip preset ids to turn on, e.g. [\"tests\", \"checkstyle\"]; get_settings with project lists them"},
			"dont_skip":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "skip preset ids the task has on to turn off for this run, e.g. [\"tests\"] to run tests"},
		}), run: (*Server).mcpRunTask},
	{Name: "get_job", Description: "A job's status, exit code, command, failed modules, problems found in the log and the end of the log. " +
		"With wait_seconds it waits for the job to finish first.",
		InputSchema: schema([]string{"job"}, map[string]any{
			"job":          prop("integer", "job id"),
			"wait_seconds": prop("integer", "wait up to this long (max 300) for a queued or running job to finish"),
			"log_lines":    prop("integer", "how many log lines to return (default 60, max 1000)"),
			"log_from":     prop("integer", "return the log from this line instead of its end, e.g. a problem's logLine minus a few"),
		}), run: (*Server).mcpGetJob},
	{Name: "list_modules", Description: "A project's Maven modules as found by the last discovery, with the ref to pass to run_task modules. " +
		"Use filter to search large projects.",
		InputSchema: schema([]string{"project"}, map[string]any{
			"project": prop("string", "project id or name"),
			"filter":  prop("string", "only modules whose ref, name or path contains this text"),
			"refresh": prop("boolean", "start a new discovery in the background (takes a while); call again later"),
		}), run: (*Server).mcpListModules},
	{Name: "list_jobs", Description: "A project's recent jobs, newest first: id, task, status, exit code, directory and times.",
		InputSchema: schema([]string{"project"}, map[string]any{
			"project": prop("string", "project id or name"),
			"limit":   prop("integer", "how many (default 10, max 50)"),
		}), run: (*Server).mcpListJobs},
	{Name: "rerun_job", Description: "Run a job again with the same settings and worktree. With without_failed it leaves out " +
		"the modules that failed and everything depending on them.",
		InputSchema: schema([]string{"job"}, map[string]any{
			"job":            prop("integer", "job id"),
			"without_failed": prop("boolean", "skip the failed modules and their dependents"),
		}), run: (*Server).mcpRerunJob},
	{Name: "cancel_job", Description: "Cancel a queued or running job (stop, then kill after a grace period).",
		InputSchema: schema([]string{"job"}, map[string]any{"job": prop("integer", "job id")}), run: (*Server).mcpCancelJob},
	{Name: "get_settings", Description: "Read-only settings for diagnosing builds: build tools found, JDKs, global defaults and the " +
		"tool config files (~/.m2/settings.xml, toolchains, .mavenrc, mvnd.properties...). With project: what its runs use " +
		"(executable, JDK, env, default resources).",
		InputSchema: schema(nil, map[string]any{"project": prop("string", "project id or name")}), run: (*Server).mcpGetSettings},
	{Name: "read_config_file", Description: "Read one tool config file listed by get_settings, by id (e.g. maven.settings). Passwords are masked.",
		InputSchema: schema([]string{"id"}, map[string]any{"id": prop("string", "config file id")}), run: (*Server).mcpReadConfig},
}

// ref is an id or a name; agents send either strings or numbers.
type ref string

func (r *ref) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) != nil {
		s = string(b)
	}
	*r = ref(strings.TrimSpace(s))
	return nil
}

func args(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("invalid arguments: %v", err)
	}
	return nil
}

func (s *Server) findProject(ctx context.Context, r ref) (store.Project, error) {
	list, err := s.Store.ListProjects(ctx)
	if err != nil {
		return store.Project{}, err
	}
	for _, p := range list {
		if strconv.FormatInt(p.ID, 10) == string(r) || strings.EqualFold(p.Name, string(r)) {
			return p, nil
		}
	}
	return store.Project{}, fmt.Errorf("no project %q; see list_projects", r)
}

type mcpTask struct {
	ID      int64             `json:"id"`
	Name    string            `json:"name"`
	Skips   []string          `json:"skips"` // skip preset ids the task has on
	Command string            `json:"command"`
	LastJob *store.JobSummary `json:"lastJob"`
}

func (s *Server) mcpListProjects(ctx context.Context, _ json.RawMessage) (any, error) {
	list, err := s.Store.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	type item struct {
		ID        int64              `json:"id"`
		Name      string             `json:"name"`
		Dir       string             `json:"dir"`
		BuildTool string             `json:"buildTool"`
		Git       *project.Git       `json:"git,omitempty"`
		Worktrees []project.Worktree `json:"worktrees,omitempty"`
		Tasks     []mcpTask          `json:"tasks"`
		LastJob   *store.JobSummary  `json:"lastJob"`
	}
	out := []item{}
	for _, p := range list {
		it := item{ID: p.ID, Name: p.Name, Dir: p.RootPath, BuildTool: p.BuildTool, LastJob: p.LastJob, Tasks: []mcpTask{}}
		info, err := s.runtimeFor(ctx, p)
		if err != nil {
			return nil, err
		}
		it.Git = info.Git
		if wts := project.Worktrees(ctx, p.RootPath); len(wts) > 1 {
			it.Worktrees = wts
		}
		exe := "mvn"
		if info.Executable != nil {
			exe = filepath.Base(info.Executable.Path)
		}
		profiles, err := s.Store.ListProfiles(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		for _, prof := range profiles {
			var spec maven.RunSpec
			_ = json.Unmarshal(prof.Spec, &spec)
			argv := spec.Normalize().WithDefaults(resourcesFor(p)).Argv(exe, presetsFor(p))
			it.Tasks = append(it.Tasks, mcpTask{ID: prof.ID, Name: prof.Name, Skips: nonNil(spec.Skips), Command: strings.Join(argv, " "), LastJob: prof.LastJob})
		}
		out = append(out, it)
	}
	return out, nil
}

func (s *Server) mcpRunTask(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Project    ref               `json:"project"`
		Task       ref               `json:"task"`
		Worktree   string            `json:"worktree"`
		Modules    []string          `json:"modules"`
		AlsoMake   bool              `json:"also_make"`
		Properties map[string]string `json:"properties"`
		ExtraArgs  []string          `json:"extra_args"`
		Skip       []string          `json:"skip"`
		DontSkip   []string          `json:"dont_skip"`
	}
	if err := args(raw, &a); err != nil {
		return nil, err
	}
	p, err := s.findProject(ctx, a.Project)
	if err != nil {
		return nil, err
	}
	profiles, err := s.Store.ListProfiles(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	var task *store.RunProfile
	for i, prof := range profiles {
		if strconv.FormatInt(prof.ID, 10) == string(a.Task) || strings.EqualFold(prof.Name, string(a.Task)) {
			task = &profiles[i]
			break
		}
	}
	if task == nil {
		return nil, fmt.Errorf("project %s has no task %q; see list_projects", p.Name, a.Task)
	}
	var spec maven.RunSpec
	if err := json.Unmarshal(task.Spec, &spec); err != nil {
		return nil, errors.New("the saved task has an invalid spec")
	}
	if len(a.Modules) > 0 {
		spec.Modules = maven.Modules{Mode: "selected", List: a.Modules, AlsoMake: a.AlsoMake}
	}
	if len(a.Properties) > 0 && spec.Properties == nil {
		spec.Properties = map[string]string{}
	}
	for k, v := range a.Properties {
		spec.Properties[k] = v
	}
	spec.ExtraArgs = append(spec.ExtraArgs, a.ExtraArgs...)
	if err := applySkips(&spec, presetsFor(p), a.Skip, a.DontSkip); err != nil {
		return nil, err
	}
	if !inWorktree(ctx, &p, a.Worktree) {
		return nil, errors.New("not a git worktree of this project: " + a.Worktree)
	}
	// Reuse the HTTP path that starts jobs, so runs from agents and the UI behave the same.
	rec := httptest.NewRecorder()
	s.start(rec, httptest.NewRequestWithContext(ctx, http.MethodPost, "/mcp", nil), p, spec, &task.ID)
	if rec.Code >= 300 {
		var e apiError
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		return nil, errors.New(e.Message)
	}
	var job jobView
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		return nil, err
	}
	return map[string]any{"job": job.ID, "status": job.Status, "dir": p.RootPath, "command": strings.Join(job.Argv, " ")}, nil
}

func (s *Server) mcpGetJob(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Job      int64 `json:"job"`
		Wait     int   `json:"wait_seconds"`
		LogLines int   `json:"log_lines"`
		LogFrom  int   `json:"log_from"`
	}
	if err := args(raw, &a); err != nil {
		return nil, err
	}
	j, err := s.Store.GetJob(ctx, a.Job)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("no job %d", a.Job)
	}
	if err != nil {
		return nil, err
	}
	if a.Wait > 0 && !j.Finished() {
		wait, cancel := context.WithTimeout(ctx, time.Duration(min(a.Wait, 300))*time.Second)
		defer cancel()
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
	poll:
		for !j.Finished() {
			select {
			case <-wait.Done():
				break poll
			case <-tick.C:
				if fresh, err := s.Store.GetJob(ctx, j.ID); err == nil {
					j = fresh
				}
			}
		}
	}

	out := map[string]any{"job": s.view(j)}
	var mods []store.JobModule
	if s.Runner != nil {
		mods = s.Runner.Modules(j.ID)
	}
	if mods == nil {
		mods, _ = s.Store.ListJobModules(ctx, j.ID)
	}
	counts := map[string]int{}
	failed := []string{}
	for _, m := range mods {
		counts[m.Status]++
		if m.Status == "failed" {
			failed = append(failed, m.Name)
		}
	}
	out["modules"] = counts
	out["failedModules"] = failed
	var probs []store.JobProblem
	if s.Runner != nil && !j.Finished() {
		probs = s.Runner.Problems(j.ID)
	}
	if probs == nil {
		probs, _ = s.Store.ListJobProblems(ctx, j.ID)
	}
	if len(probs) > 30 {
		out["problemsTotal"] = len(probs)
		probs = probs[:30]
	}
	out["problems"] = nonNil(probs)

	n := a.LogLines
	if n <= 0 {
		n = 60
	}
	n = min(n, 1000)
	from := a.LogFrom
	if from <= 0 {
		_, total, _ := runner.ReadLines(j.LogPath, 1, 1)
		from = max(1, total-n+1)
	}
	lines, total, _ := runner.ReadLines(j.LogPath, from, n)
	out["log"] = logPage{From: from, Lines: nonNil(lines), Total: total}
	return out, nil
}

func (s *Server) mcpCancelJob(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Job int64 `json:"job"`
	}
	if err := args(raw, &a); err != nil {
		return nil, err
	}
	j, err := s.Store.GetJob(ctx, a.Job)
	if err != nil {
		return nil, fmt.Errorf("no job %d", a.Job)
	}
	if err := s.Runner.Cancel(j.ID); err != nil {
		return nil, fmt.Errorf("job %d is already %s", j.ID, j.Status)
	}
	if fresh, err := s.Store.GetJob(ctx, j.ID); err == nil {
		j = fresh
	}
	return map[string]any{"job": j.ID, "status": j.Status}, nil
}

func (s *Server) mcpGetSettings(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Project ref `json:"project"`
	}
	if err := args(raw, &a); err != nil {
		return nil, err
	}
	settings, err := s.Store.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	jdks, err := s.Store.ListJDKs(ctx)
	if err != nil {
		return nil, err
	}
	// The tools list is what Settings shows: launchers on PATH, versions, wrappers.
	rec := httptest.NewRecorder()
	s.getTools(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/tools", nil))
	type file struct {
		ID          string `json:"id"`
		Tool        string `json:"tool"`
		Path        string `json:"path"`
		Description string `json:"description"`
		Exists      bool   `json:"exists"`
	}
	files := []file{}
	for _, f := range s.toolConfigs().List() {
		files = append(files, file{f.ID, f.Tool, f.Path, f.Description, f.Exists})
	}
	out := map[string]any{"defaults": settings, "jdks": jdks, "tools": json.RawMessage(rec.Body.Bytes()), "configFiles": files}
	if a.Project != "" {
		p, err := s.findProject(ctx, a.Project)
		if err != nil {
			return nil, err
		}
		info, err := s.runtimeFor(ctx, p)
		if err != nil {
			return nil, err
		}
		out["project"] = map[string]any{"id": p.ID, "name": p.Name, "dir": p.RootPath, "env": p.Env,
			"resources": p.Resources, "skipPresets": presetsFor(p), "runtime": info}
	}
	return out, nil
}

// secretRe matches the values of password-like XML elements and properties.
var secretRe = regexp.MustCompile(`(?i)(<(password|passphrase|privateKey)>)[^<]*(</)|((password|passphrase|token|secret)\s*[=:]\s*)\S+`)

func (s *Server) mcpReadConfig(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := args(raw, &a); err != nil {
		return nil, err
	}
	c, err := s.toolConfigs().Read(a.ID)
	if err != nil {
		return nil, err
	}
	c.Content = secretRe.ReplaceAllString(c.Content, "${1}${4}***${3}")
	c.Template = ""
	return c, nil
}

func (s *Server) mcpListModules(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Project ref    `json:"project"`
		Filter  string `json:"filter"`
		Refresh bool   `json:"refresh"`
	}
	if err := args(raw, &a); err != nil {
		return nil, err
	}
	p, err := s.findProject(ctx, a.Project)
	if err != nil {
		return nil, err
	}
	if a.Refresh {
		if _, err := s.SubmitModuleDiscovery(p, maven.RunSpec{}); err != nil {
			return nil, err
		}
		return map[string]any{"discovery": "started; call list_modules again in a minute"}, nil
	}
	mods, err := s.Store.ListModules(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	perArtifact := map[string]int{}
	for _, m := range mods {
		perArtifact[m.ArtifactID]++
	}
	type module struct {
		Ref       string `json:"ref"` // what -pl takes
		Name      string `json:"name"`
		Path      string `json:"path"`
		Packaging string `json:"packaging"`
	}
	out := []module{}
	filter := strings.ToLower(a.Filter)
	for _, m := range mods {
		r := ":" + m.ArtifactID
		if perArtifact[m.ArtifactID] > 1 {
			r = m.Key
		}
		it := module{r, m.Name, m.RelPath, m.Packaging}
		if filter == "" || strings.Contains(strings.ToLower(r+" "+m.Name+" "+m.RelPath), filter) {
			out = append(out, it)
		}
	}
	res := map[string]any{"total": len(mods), "modules": out}
	if len(mods) == 0 {
		res["hint"] = "no modules discovered yet; call list_modules with refresh true"
	} else {
		res["discoveredAt"] = mods[0].DiscoveredAt
	}
	return res, nil
}

func (s *Server) mcpListJobs(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Project ref `json:"project"`
		Limit   int `json:"limit"`
	}
	if err := args(raw, &a); err != nil {
		return nil, err
	}
	p, err := s.findProject(ctx, a.Project)
	if err != nil {
		return nil, err
	}
	if a.Limit <= 0 {
		a.Limit = 10
	}
	jobs, err := s.Store.ListJobs(ctx, p.ID, min(a.Limit, 50))
	if err != nil {
		return nil, err
	}
	profiles, err := s.Store.ListProfiles(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	names := map[int64]string{}
	for _, prof := range profiles {
		names[prof.ID] = prof.Name
	}
	type job struct {
		ID         int64      `json:"id"`
		Task       string     `json:"task"` // "" for a run that was not a saved task
		Status     string     `json:"status"`
		ExitCode   *int       `json:"exitCode"`
		Dir        string     `json:"dir"`
		QueuedAt   time.Time  `json:"queuedAt"`
		FinishedAt *time.Time `json:"finishedAt"`
	}
	out := []job{}
	for _, j := range jobs {
		it := job{ID: j.ID, Status: j.Status, ExitCode: j.ExitCode, Dir: j.Dir, QueuedAt: j.QueuedAt, FinishedAt: j.FinishedAt}
		if j.ProfileID != nil {
			it.Task = names[*j.ProfileID]
		}
		out = append(out, it)
	}
	return out, nil
}

func (s *Server) mcpRerunJob(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Job           int64 `json:"job"`
		WithoutFailed bool  `json:"without_failed"`
	}
	if err := args(raw, &a); err != nil {
		return nil, err
	}
	target := "/api/jobs/" + strconv.FormatInt(a.Job, 10) + "/rerun"
	if a.WithoutFailed {
		target += "?without=failed"
	}
	// The UI's rerun handler, so both pick the same worktree and modules.
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	r.SetPathValue("id", strconv.FormatInt(a.Job, 10))
	rec := httptest.NewRecorder()
	s.rerunJob(rec, r)
	if rec.Code >= 300 {
		var e apiError
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		return nil, errors.New(e.Message)
	}
	var job jobView
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		return nil, err
	}
	return map[string]any{"job": job.ID, "status": job.Status, "dir": job.Dir, "command": strings.Join(job.Argv, " ")}, nil
}

// applySkips turns skip presets on and off for one run; unknown ids are an error that lists the known ones.
func applySkips(spec *maven.RunSpec, presets []maven.SkipPreset, on, off []string) error {
	known := map[string]bool{}
	var ids []string
	for _, p := range presets {
		known[p.ID] = true
		ids = append(ids, p.ID)
	}
	for _, id := range append(append([]string{}, on...), off...) {
		if !known[id] {
			return fmt.Errorf("no skip preset %q; this project has: %s", id, strings.Join(ids, ", "))
		}
	}
	drop := map[string]bool{}
	for _, id := range off {
		drop[id] = true
	}
	var out []string
	for _, id := range append(spec.Skips, on...) {
		if !drop[id] && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	spec.Skips = out
	return nil
}
