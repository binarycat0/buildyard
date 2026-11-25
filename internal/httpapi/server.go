// Package httpapi serves the JSON API and the embedded UI on 127.0.0.1.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/binarycat0/buildyard/internal/config"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"time"

	"github.com/binarycat0/buildyard/internal/jdk"
	"github.com/binarycat0/buildyard/internal/runner"
	"github.com/binarycat0/buildyard/internal/store"
	"github.com/binarycat0/buildyard/internal/tasks"
	"github.com/binarycat0/buildyard/internal/telemetry"
)

// Version is set by main from build flags.
var Version = "dev"

// Server holds what the handlers need.
type Server struct {
	Store   *store.Store
	Tasks   *tasks.Manager
	Runner  *runner.Runner
	JDKs    jdk.Detector // where to look for JDKs
	Home    string       // user home, for finding mvn/mvnd outside PATH
	DataDir string
	Config  *config.Live // daemon settings from config.toml; nil in tests that don't need them
	ExtJar  string       // installed Maven extension; "" disables it
	// Telemetry is the OpenTelemetry export; nil in tests that don't need it.
	Telemetry *telemetry.Exporter
	UI        fs.FS               // static files; index.html at the root
	Getenv    func(string) string // env lookup for config file locations; nil = os.Getenv
	Log       *slog.Logger
	started   time.Time
}

// Handler returns the HTTP handler with the API and UI routes.
func (s *Server) Handler() http.Handler {
	s.started = time.Now()
	if s.Log == nil {
		s.Log = slog.Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/projects", s.listProjects)
	mux.HandleFunc("POST /api/projects", s.createProject)
	mux.HandleFunc("GET /api/projects/{id}", s.getProject)
	mux.HandleFunc("PATCH /api/projects/{id}", s.patchProject)
	mux.HandleFunc("DELETE /api/projects/{id}", s.deleteProject)
	mux.HandleFunc("GET /api/projects/{id}/runtime", s.projectRuntime)
	mux.HandleFunc("GET /api/projects/{id}/worktrees", s.listWorktrees)
	mux.HandleFunc("GET /api/projects/{id}/modules", s.listModules)
	mux.HandleFunc("POST /api/projects/{id}/modules/refresh", s.refreshModules)
	mux.HandleFunc("GET /api/projects/{id}/profiles", s.listProfiles)
	mux.HandleFunc("POST /api/projects/{id}/profiles", s.createProfile)
	mux.HandleFunc("PUT /api/profiles/{id}", s.updateProfile)
	mux.HandleFunc("DELETE /api/profiles/{id}", s.deleteProfile)
	mux.HandleFunc("POST /api/projects/{id}/preview", s.previewJob)
	mux.HandleFunc("GET /api/projects/{id}/jobs", s.listJobs)
	mux.HandleFunc("POST /api/projects/{id}/jobs", s.createJob)
	mux.HandleFunc("GET /api/jobs/{id}", s.getJob)
	mux.HandleFunc("POST /api/jobs/{id}/cancel", s.controlJob("cancel"))
	mux.HandleFunc("POST /api/jobs/{id}/stop", s.controlJob("stop"))
	mux.HandleFunc("POST /api/jobs/{id}/kill", s.controlJob("kill"))
	mux.HandleFunc("POST /api/jobs/{id}/rerun", s.rerunJob)
	mux.HandleFunc("GET /api/jobs/{id}/log", s.jobLog)
	mux.HandleFunc("GET /api/jobs/{id}/modules", s.jobModules)
	mux.HandleFunc("GET /api/jobs/{id}/problems", s.jobProblems)
	mux.HandleFunc("GET /api/jobs/{id}/stream", s.jobStream)
	mux.HandleFunc("GET /api/config", s.getConfig)
	mux.HandleFunc("PUT /api/config", s.putConfig)
	mux.HandleFunc("GET /api/telemetry", s.getTelemetry)
	mux.HandleFunc("PUT /api/telemetry", s.putTelemetry)
	mux.HandleFunc("POST /api/telemetry/test", s.testTelemetry)
	mux.HandleFunc("GET /api/settings", s.getSettings)
	mux.HandleFunc("PUT /api/settings", s.putSettings)
	mux.HandleFunc("GET /api/tools", s.getTools)
	mux.HandleFunc("POST /api/tools/detect", s.detectTools)
	mux.HandleFunc("POST /api/tools/mvnd/{action}", s.mvndCommand)
	mux.HandleFunc("GET /api/tool-configs", s.listToolConfigs)
	mux.HandleFunc("GET /api/tool-configs/{id}", s.getToolConfig)
	mux.HandleFunc("PUT /api/tool-configs/{id}", s.putToolConfig)
	mux.HandleFunc("GET /api/tool-configs/{id}/backups", s.listToolConfigBackups)
	mux.HandleFunc("GET /api/tool-configs/{id}/backups/{name}", s.getToolConfigBackup)
	mux.HandleFunc("GET /api/toolchains/generate", s.generateToolchains)
	mux.HandleFunc("GET /api/jdk-managers", s.listJDKManagers)
	mux.HandleFunc("GET /api/jdks", s.listJDKs)
	mux.HandleFunc("POST /api/jdks", s.addJDK)
	mux.HandleFunc("POST /api/jdks/detect", s.detectJDKs)
	mux.HandleFunc("PATCH /api/jdks/{id}", s.renameJDK)
	mux.HandleFunc("DELETE /api/jdks/{id}", s.deleteJDK)
	mux.HandleFunc("GET /api/tasks", s.listTasks)
	mux.HandleFunc("GET /api/tasks/{id}", s.getTask)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})
	mux.Handle("/", s.ui())
	return mux
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"version":   Version,
		"goVersion": runtime.Version(),
		"dataDir":   s.DataDir,
		"uptimeSec": int(time.Since(s.started).Seconds()),
		"startedAt": s.started.UTC(), // a change tells an open page it runs an older UI
	})
}

// ui serves the embedded static files; unknown paths fall back to index.html.
func (s *Server) ui() http.Handler {
	files := http.FileServerFS(s.UI)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		if r.URL.Path != "/" {
			if _, err := fs.Stat(s.UI, r.URL.Path[1:]); err == nil {
				files.ServeHTTP(w, r)
				return
			}
		}
		http.ServeFileFS(w, r, s.UI, "index.html")
	})
}

type apiError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiError{Error: code, Message: msg})
}

// decode reads a JSON body (max 1 MiB) into v, rejecting unknown fields.
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// ListenAndServe serves h on 127.0.0.1:port until ctx is done. Binding elsewhere is not supported.
func ListenAndServe(ctx context.Context, port int, h http.Handler) error {
	srv := &http.Server{
		Addr:              "127.0.0.1:" + strconv.Itoa(port),
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		// Handlers see ctx end, so live log streams close on shutdown.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := srv.Shutdown(shut)
		if e := <-errc; !errors.Is(e, http.ErrServerClosed) && err == nil {
			err = e
		}
		return err
	}
}
