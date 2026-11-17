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

// DetectJDKs runs a JDK scan and syncs the result into the store. Used by the task and at startup.
func DetectJDKs(ctx context.Context, st *store.Store, d jdk.Detector) (store.SyncResult, error) {
	found := d.Detect()
	if err := ctx.Err(); err != nil {
		return store.SyncResult{}, err
	}
	return st.SyncDetectedJDKs(ctx, found, func(home string) bool {
		_, err := jdk.Inspect(home)
		return err == nil
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
	if st.DefaultExecutable == "" {
		st.DefaultExecutable = maven.ModeAuto
	}
	if !maven.ValidMode(st.DefaultExecutable) {
		writeError(w, http.StatusBadRequest, "bad_request", "defaultExecutable must be auto, wrapper, mvnd, mvn or an absolute path")
		return
	}
	if st.DefaultJdkID != nil && *st.DefaultJdkID <= 0 {
		st.DefaultJdkID = nil
	}
	if st.DefaultJdkID != nil {
		if _, err := s.Store.GetJDK(r.Context(), *st.DefaultJdkID); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "unknown defaultJdkId")
			return
		}
	}
	if err := s.Store.SaveSettings(r.Context(), st); err != nil {
		s.internal(w, err)
		return
	}
	s.getSettings(w, r)
}

func (s *Server) getTools(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"tools":    maven.FindTools(s.Home),
		"javaHome": os.Getenv("JAVA_HOME"),
	})
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
	JDKFrom         string          `json:"jdkFrom"`        // project | default | environment
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

	mode := settings.DefaultExecutable
	info.ExecutableFrom = "default"
	if p.Executable != nil {
		mode, info.ExecutableFrom = *p.Executable, "project"
	}
	if res, err := maven.Resolve(mode, p.RootPath, info.Tools); err != nil {
		info.ExecutableError = err.Error()
	} else {
		info.Executable = &res
	}

	jdkID := settings.DefaultJdkID
	info.JDKFrom = "default"
	if p.JdkID != nil {
		jdkID, info.JDKFrom = p.JdkID, "project"
	}
	if jdkID == nil {
		info.JDKFrom = "environment"
	} else if j, err := s.Store.GetJDK(ctx, *jdkID); err == nil {
		info.JDK = &j
	}
	writeJSON(w, http.StatusOK, info)
}
