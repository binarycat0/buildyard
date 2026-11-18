package httpapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/binarycat0/buildyard/internal/buildtool/maven"
	"github.com/binarycat0/buildyard/internal/jdk"
	"github.com/binarycat0/buildyard/internal/store"
	"github.com/binarycat0/buildyard/internal/tasks"
)

// TaskJDKDetect is the background task kind that rescans installed JDKs.
const TaskJDKDetect = "jdk.detect"

// TaskToolsDetect is the background task kind that finds mvn and mvnd and their versions.
const TaskToolsDetect = "tools.detect"

// Keys under which detection results are cached in the setting table.
const (
	keyJDKManagers = "detected.jdkManagers"
	keyBuildTools  = "detected.buildTools"
)

// DetectJDKs runs a JDK scan, syncs the result into the store and caches the list of
// JDK managers. Used by the task and at startup.
func DetectJDKs(ctx context.Context, st *store.Store, d jdk.Detector) (store.SyncResult, error) {
	found := d.Detect()
	managers := d.Managers()
	if err := ctx.Err(); err != nil {
		return store.SyncResult{}, err
	}
	if err := st.PutJSON(ctx, keyJDKManagers, managers); err != nil {
		return store.SyncResult{}, err
	}
	return st.SyncDetectedJDKs(ctx, found, func(home string) bool {
		_, err := jdk.Inspect(home)
		return err == nil
	})
}

// SubmitToolsDetect enqueues detection of mvn/mvnd (deduplicated: one at a time).
func (s *Server) SubmitToolsDetect() (tasks.Task, error) {
	return s.Tasks.Submit(TaskToolsDetect, TaskToolsDetect, func(ctx context.Context) (any, error) {
		tools := maven.DetectTools(s.Home)
		return tools, s.Store.PutJSON(ctx, keyBuildTools, tools)
	})
}

// SubmitJDKDetect enqueues a JDK scan (deduplicated: one at a time).
func (s *Server) SubmitJDKDetect() (tasks.Task, error) {
	return s.Tasks.Submit(TaskJDKDetect, TaskJDKDetect, func(ctx context.Context) (any, error) {
		return DetectJDKs(ctx, s.Store, s.JDKs)
	})
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.Store.GetSettings(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var st store.Settings
	if err := decode(r, &st); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	known := store.KnownTools()
	for id, t := range st.Tools {
		def, ok := known[id]
		if !ok {
			writeError(w, http.StatusBadRequest, "bad_request", "unknown build tool "+id)
			return
		}
		if t.Executable == "" {
			t.Executable = def.Executable
		}
		if id == "maven" && !maven.ValidMode(t.Executable) {
			writeError(w, http.StatusBadRequest, "bad_request", "maven executable must be auto, wrapper, mvnd, mvn or an absolute path")
			return
		}
		if t.JdkID != nil && *t.JdkID <= 0 {
			t.JdkID = nil
		}
		if t.JdkID != nil {
			if _, err := s.Store.GetJDK(r.Context(), *t.JdkID); err != nil {
				writeError(w, http.StatusBadRequest, "bad_request", "unknown jdkId for "+id)
				return
			}
		}
		st.Tools[id] = t
	}
	if err := s.Store.SaveSettings(r.Context(), st); err != nil {
		s.internal(w, err)
		return
	}
	s.getSettings(w, r)
}

// projectWrapper is a project's ./mvnw and the Maven version it pins.
type projectWrapper struct {
	ProjectID    int64  `json:"projectId"`
	Project      string `json:"project"`
	MavenVersion string `json:"mavenVersion"`
}

// getTools returns the launchers found on PATH (live), their versions (from the last
// tools.detect task) and the Maven wrappers of registered projects.
func (s *Server) getTools(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var installed []maven.InstalledTool
	if _, err := s.Store.GetJSON(ctx, keyBuildTools, &installed); err != nil {
		s.internal(w, err)
		return
	}
	projects, err := s.Store.ListProjects(ctx)
	if err != nil {
		s.internal(w, err)
		return
	}
	wrappers := []projectWrapper{}
	for _, p := range projects {
		if wr := maven.FindWrapper(p.RootPath); wr != nil {
			wrappers = append(wrappers, projectWrapper{ProjectID: p.ID, Project: p.Name, MavenVersion: wr.MavenVersion})
		}
	}
	if installed == nil {
		installed = []maven.InstalledTool{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tools":     maven.FindTools(s.Home),
		"installed": installed,
		"wrappers":  wrappers,
		"javaHome":  os.Getenv("JAVA_HOME"),
	})
}

func (s *Server) detectTools(w http.ResponseWriter, r *http.Request) {
	t, err := s.SubmitToolsDetect()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "busy", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, t)
}

// jdkManager is a JDK manager with the number of known JDKs that came from it.
type jdkManager struct {
	jdk.Manager
	JDKs int `json:"jdks"`
}

func (s *Server) listJDKManagers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var managers []jdk.Manager
	if _, err := s.Store.GetJSON(ctx, keyJDKManagers, &managers); err != nil {
		s.internal(w, err)
		return
	}
	jdks, err := s.Store.ListJDKs(ctx)
	if err != nil {
		s.internal(w, err)
		return
	}
	count := map[string]int{}
	for _, j := range jdks {
		if !j.Missing {
			count[j.Source]++
		}
	}
	out := make([]jdkManager, 0, len(managers))
	for _, m := range managers {
		out = append(out, jdkManager{Manager: m, JDKs: count[m.ID]})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listJDKs(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListJDKs(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) detectJDKs(w http.ResponseWriter, r *http.Request) {
	t, err := s.SubmitJDKDetect()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "busy", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, t)
}

func (s *Server) addJDK(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Home string `json:"home"`
		Name string `json:"name"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	info, err := jdk.Inspect(strings.TrimSpace(req.Home))
	if err != nil {
		writeError(w, http.StatusBadRequest, "not_jdk", err.Error())
		return
	}
	j, err := s.Store.AddJDK(r.Context(), info, strings.TrimSpace(req.Name))
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "duplicate_jdk", "this JDK is already in the list")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, j)
}

func (s *Server) renameJDK(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	j, err := s.Store.RenameJDK(r.Context(), id, strings.TrimSpace(req.Name))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "JDK not found")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func (s *Server) deleteJDK(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	err := s.Store.DeleteJDK(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "JDK not found")
	case err != nil:
		s.internal(w, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Tasks.List())
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	t, found := s.Tasks.Get(id)
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "task not found")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// runtimeInfo is what a run of this project will use, with the alternatives for the settings UI.
type runtimeInfo struct {
	Executable      *maven.Resolved `json:"executable"`
	ExecutableError string          `json:"executableError,omitempty"`
	ExecutableFrom  string          `json:"executableFrom"` // project | default
	JDK             *store.JDK      `json:"jdk"`            // nil = daemon's own JAVA_HOME
	JDKFrom         string          `json:"jdkFrom"`        // tool (project's build tool JDK) | project (project JDK) | default | environment
	ProjectJDK      *store.JDK      `json:"projectJdk"`     // the project JDK, if set
	Wrapper         *maven.Wrapper  `json:"wrapper"`
	Tools           maven.Tools     `json:"tools"`
}

func (s *Server) projectRuntime(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	p, err := s.Store.GetProject(ctx, id)
	if err != nil {
		s.respondProject(w, p, err)
		return
	}
	settings, err := s.Store.GetSettings(ctx)
	if err != nil {
		s.internal(w, err)
		return
	}
	info := runtimeInfo{Tools: maven.FindTools(s.Home), Wrapper: maven.FindWrapper(p.RootPath)}

	tool := settings.Tool(p.BuildTool)
	mode := tool.Executable
	info.ExecutableFrom = "default"
	if p.Executable != nil {
		mode, info.ExecutableFrom = *p.Executable, "project"
	}
	if res, err := maven.Resolve(mode, p.RootPath, info.Tools); err != nil {
		info.ExecutableError = err.Error()
	} else {
		info.Executable = &res
	}

	// Same order as IntelliJ: the tool's own JDK, else the project JDK, else the tool's global default.
	jdkID := tool.JdkID
	info.JDKFrom = "default"
	if p.JdkID != nil {
		jdkID, info.JDKFrom = p.JdkID, "project"
		if j, err := s.Store.GetJDK(ctx, *p.JdkID); err == nil {
			info.ProjectJDK = &j
		}
	}
	if p.ToolJdkID != nil {
		jdkID, info.JDKFrom = p.ToolJdkID, "tool"
	}
	if jdkID == nil {
		info.JDKFrom = "environment"
	} else if j, err := s.Store.GetJDK(ctx, *jdkID); err == nil {
		info.JDK = &j
	}
	writeJSON(w, http.StatusOK, info)
}
