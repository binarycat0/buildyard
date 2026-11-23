package httpapi

import (
	"net/http"

	"github.com/binarycat0/buildyard/internal/config"
	"github.com/binarycat0/buildyard/internal/runner"
)

// configView is the daemon part of config.toml as the Settings page shows it.
type configView struct {
	config.Limits
	Port int    `json:"port"` // read-only here: changing it needs a restart
	Path string `json:"path"` // config.toml
	// KeepAwakeAvailable: this machine can keep itself awake (macOS caffeinate).
	KeepAwakeAvailable bool `json:"keepAwakeAvailable"`
}

func (s *Server) configView() configView {
	return configView{Limits: s.Config.Limits(), Port: s.Config.Get().Port, Path: s.Config.Path(),
		KeepAwakeAvailable: runner.KeepAwakeAvailable()}
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	if s.Config == nil {
		writeError(w, http.StatusNotFound, "not_found", "no config")
		return
	}
	writeJSON(w, http.StatusOK, s.configView())
}

// putConfig saves the limits to config.toml and applies them at once.
func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	if s.Config == nil {
		writeError(w, http.StatusNotFound, "not_found", "no config")
		return
	}
	var req config.Limits
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if err := s.Config.SetLimits(req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.configView())
}
