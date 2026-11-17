package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/binarycat0/buildyard/internal/project"
	"github.com/binarycat0/buildyard/internal/store"
)

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListProjects(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type createProjectReq struct {
	Name     string `json:"name"`
	RootPath string `json:"rootPath"`
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectReq
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	root, err := project.Inspect(strings.TrimSpace(req.RootPath))
	if err != nil {
		code := "bad_path"
		if errors.Is(err, project.ErrUnsupported) {
			code = "unsupported_build_tool"
		} else if errors.Is(err, project.ErrNoBuildFile) {
			code = "no_build_file"
		}
		writeError(w, http.StatusBadRequest, code, err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = filepath.Base(root.Path)
	}
	p, err := s.Store.CreateProject(r.Context(), store.Project{Name: name, RootPath: root.Path, BuildTool: root.BuildTool})
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "duplicate_root", "a project with this root path already exists")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	s.Log.Info("project added", "id", p.ID, "name", p.Name, "root", p.RootPath)
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p, err := s.Store.GetProject(r.Context(), id)
	s.respondProject(w, p, err)
}

func (s *Server) patchProject(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var patch store.ProjectPatch
	if err := decode(r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if patch.Name != nil && strings.TrimSpace(*patch.Name) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "name must not be empty")
		return
	}
	if patch.SkipPresets != nil {
		var arr []json.RawMessage
		if json.Unmarshal(*patch.SkipPresets, &arr) != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "skipPresets must be a JSON array")
			return
		}
	}
	if patch.JavaHome != nil && *patch.JavaHome != "" && !filepath.IsAbs(*patch.JavaHome) {
		writeError(w, http.StatusBadRequest, "bad_request", "javaHome must be an absolute path")
		return
	}
	p, err := s.Store.UpdateProject(r.Context(), id, patch)
	s.respondProject(w, p, err)
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	err := s.Store.DeleteProject(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "project not found")
	case err != nil:
		s.internal(w, err)
	default:
		s.Log.Info("project deleted", "id", id)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) respondProject(w http.ResponseWriter, p store.Project, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "project not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case err != nil:
		s.internal(w, err)
	default:
		writeJSON(w, http.StatusOK, p)
	}
}

func (s *Server) internal(w http.ResponseWriter, err error) {
	s.Log.Error("request failed", "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid id")
		return 0, false
	}
	return id, true
}
