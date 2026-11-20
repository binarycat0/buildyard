package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/binarycat0/buildyard/internal/config"
	"github.com/binarycat0/buildyard/internal/telemetry"
)

// TaskTelemetryTest is the background task that sends a test span and log record.
const TaskTelemetryTest = "telemetry.test"

// telemetryView is the Observability tab: settings from config.toml and export counters.
type telemetryView struct {
	config.Telemetry
	Status telemetry.Status `json:"status"`
	Path   string           `json:"path"` // config.toml
}

func (s *Server) telemetryReady(w http.ResponseWriter) bool {
	if s.Config == nil || s.Telemetry == nil {
		writeError(w, http.StatusNotFound, "not_found", "telemetry is not available")
		return false
	}
	return true
}

func (s *Server) telemetryView() telemetryView {
	t := s.Config.Get().Telemetry
	if t.Headers == nil {
		t.Headers = map[string]string{}
	}
	return telemetryView{Telemetry: t, Status: s.Telemetry.Status(), Path: s.Config.Path()}
}

func (s *Server) getTelemetry(w http.ResponseWriter, r *http.Request) {
	if !s.telemetryReady(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.telemetryView())
}

// putTelemetry saves the settings to config.toml; builds that start afterwards use them.
func (s *Server) putTelemetry(w http.ResponseWriter, r *http.Request) {
	if !s.telemetryReady(w) {
		return
	}
	var req config.Telemetry
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if err := s.Config.SetTelemetry(req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.telemetryView())
}

// testTelemetry sends a test span and log record with the given (maybe unsaved)
// settings, in the background; poll the returned task.
func (s *Server) testTelemetry(w http.ResponseWriter, r *http.Request) {
	if !s.telemetryReady(w) {
		return
	}
	var req config.Telemetry
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	task, err := s.Tasks.Submit(TaskTelemetryTest, TaskTelemetryTest, func(ctx context.Context) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		return map[string]bool{"ok": true}, s.Telemetry.Test(ctx, req)
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, task)
}
