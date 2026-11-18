package httpapi

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/binarycat0/buildyard/internal/buildtool/maven"
	"github.com/binarycat0/buildyard/internal/toolconfig"
)

// TaskMvnd is the background task kind that runs `mvnd --status` or `mvnd --stop`.
const TaskMvnd = "mvnd.daemons"

func (s *Server) toolConfigs() toolconfig.Catalog {
	return toolconfig.Catalog{Home: s.Home, BackupDir: filepath.Join(s.DataDir, "backups"), Getenv: s.Getenv}
}

func (s *Server) respondToolConfig(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, toolconfig.ErrUnknown), errors.Is(err, fs.ErrNotExist):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, toolconfig.ErrConflict):
		writeError(w, http.StatusConflict, "changed_on_disk", err.Error()+"; reload it, then apply your change again")
	case errors.Is(err, toolconfig.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_content", err.Error())
	default:
		s.internal(w, err)
	}
}

func (s *Server) listToolConfigs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.toolConfigs().List())
}

func (s *Server) getToolConfig(w http.ResponseWriter, r *http.Request) {
	c, err := s.toolConfigs().Read(r.PathValue("id"))
	if err != nil {
		s.respondToolConfig(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) putToolConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Content string `json:"content"`
		BaseSHA string `json:"baseSha"` // the sha the editor loaded; "" for a new file
	}
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	c, backup, err := s.toolConfigs().Write(r.PathValue("id"), req.Content, req.BaseSHA)
	if err != nil {
		s.respondToolConfig(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"file": c, "backup": backup})
}

func (s *Server) listToolConfigBackups(w http.ResponseWriter, r *http.Request) {
	list, err := s.toolConfigs().Backups(r.PathValue("id"))
	if err != nil {
		s.respondToolConfig(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) getToolConfigBackup(w http.ResponseWriter, r *http.Request) {
	content, err := s.toolConfigs().ReadBackup(r.PathValue("id"), r.PathValue("name"))
	if err != nil {
		s.respondToolConfig(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": r.PathValue("name"), "content": content})
}

// generateToolchains renders a toolchains.xml from the known JDKs (not written; the
// editor shows it so the user can review and save).
func (s *Server) generateToolchains(w http.ResponseWriter, r *http.Request) {
	jdks, err := s.Store.ListJDKs(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	var list []toolconfig.ToolchainJDK
	for _, j := range jdks {
		if !j.Missing {
			list = append(list, toolconfig.ToolchainJDK{Home: j.Home, Version: j.Version, Major: j.Major, Vendor: j.Vendor})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"content": toolconfig.Toolchains(list), "jdks": len(list)})
}

// mvndCommand runs `mvnd --status` or `mvnd --stop` as a background task. mvnd needs a
// JDK, so it gets the global Maven JDK when one is set.
func (s *Server) mvndCommand(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	if action != "status" && action != "stop" {
		writeError(w, http.StatusNotFound, "not_found", "unknown mvnd action")
		return
	}
	bin := maven.FindTools(s.Home).Mvnd
	if bin == "" {
		writeError(w, http.StatusBadRequest, "not_installed", "mvnd is not installed")
		return
	}
	env := os.Environ()
	if st, err := s.Store.GetSettings(r.Context()); err == nil {
		if id := st.Tool("maven").JdkID; id != nil {
			if j, err := s.Store.GetJDK(r.Context(), *id); err == nil && !j.Missing {
				env = append(env, "JAVA_HOME="+j.Home)
			}
		}
	}
	t, err := s.Tasks.Submit(TaskMvnd, TaskMvnd, func(ctx context.Context) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, "--"+action)
		cmd.Env = env
		cmd.Dir = s.Home
		out, err := cmd.CombinedOutput()
		res := map[string]string{"action": action, "output": strings.TrimSpace(string(out))}
		if err != nil {
			res["error"] = err.Error()
		}
		return res, nil
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "busy", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, t)
}
