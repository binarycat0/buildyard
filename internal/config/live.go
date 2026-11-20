package config

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

// Live holds the daemon's config while it runs. Settings the web UI changes
// (or a SIGHUP reload picks up) reach the parts that use them through the
// OnChange hooks, so no restart is needed. The port is the exception: a
// running daemon keeps the one it listens on.
type Live struct {
	mu    sync.Mutex
	path  string
	cfg   Config
	hooks []func(Config)
}

func NewLive(path string, cfg Config) *Live { return &Live{path: path, cfg: cfg} }

// Path is the config.toml this config is saved to.
func (l *Live) Path() string { return l.path }

func (l *Live) Get() Config {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cfg
}

// OnChange registers f to be called with the new config after every change.
func (l *Live) OnChange(f func(Config)) {
	l.mu.Lock()
	l.hooks = append(l.hooks, f)
	l.mu.Unlock()
}

// Limits are the settings the web UI edits.
type Limits struct {
	MaxConcurrentJobs int `json:"maxConcurrentJobs"`
	JobsPerProject    int `json:"jobsPerProject"` // finished jobs kept per project; 0 = all
}

func (l *Live) Limits() Limits {
	c := l.Get()
	return Limits{MaxConcurrentJobs: c.MaxConcurrentJobs, JobsPerProject: c.Retention.JobsPerProject}
}

// SetLimits validates the limits, writes them to config.toml and applies them.
func (l *Live) SetLimits(v Limits) error {
	if v.JobsPerProject < 0 {
		return errors.New("jobsPerProject must be >= 0 (0 keeps all)")
	}
	l.mu.Lock()
	next := l.cfg
	next.MaxConcurrentJobs = v.MaxConcurrentJobs
	next.Retention.JobsPerProject = v.JobsPerProject
	if err := next.Validate(); err != nil {
		l.mu.Unlock()
		return err
	}
	err := updateFile(l.path, func(raw map[string]any) {
		raw["maxConcurrentJobs"] = v.MaxConcurrentJobs
		section(raw, "retention")["jobsPerProject"] = v.JobsPerProject
	})
	if err != nil {
		l.mu.Unlock()
		return err
	}
	return l.setLocked(next)
}

// SetTelemetry validates the telemetry settings, writes them to config.toml and applies them.
func (l *Live) SetTelemetry(t Telemetry) error {
	t.Endpoint = strings.TrimRight(strings.TrimSpace(t.Endpoint), "/")
	t.GrafanaURL = strings.TrimRight(strings.TrimSpace(t.GrafanaURL), "/")
	t.ServiceName = strings.TrimSpace(t.ServiceName)
	if t.ServiceName == "" {
		t.ServiceName = DefaultTelemetry().ServiceName
	}
	if err := t.Validate(); err != nil {
		return err
	}
	l.mu.Lock()
	next := l.cfg
	next.Telemetry = t
	err := updateFile(l.path, func(raw map[string]any) {
		sec := section(raw, "telemetry")
		sec["enabled"] = t.Enabled
		sec["endpoint"] = t.Endpoint
		sec["traces"] = t.Traces
		sec["logs"] = t.Logs
		sec["serviceName"] = t.ServiceName
		sec["grafanaUrl"] = t.GrafanaURL
		if len(t.Headers) > 0 {
			sec["headers"] = t.Headers
		} else {
			delete(sec, "headers")
		}
	})
	if err != nil {
		l.mu.Unlock()
		return err
	}
	return l.setLocked(next)
}

// Reload reads config.toml again, e.g. after the user edited it.
func (l *Live) Reload() error {
	next, err := Load(l.path)
	if err != nil {
		return err
	}
	l.mu.Lock()
	next.Port = l.cfg.Port // the listener can't move
	return l.setLocked(next)
}

// setLocked stores next, unlocks, then runs the hooks outside the lock.
func (l *Live) setLocked(next Config) error {
	l.cfg = next
	hooks := append([]func(Config){}, l.hooks...)
	l.mu.Unlock()
	for _, h := range hooks {
		h(next)
	}
	return nil
}

// updateFile edits config.toml as a generic table, so keys this version does
// not know survive. Comments are not kept: the toml encoder drops them.
func updateFile(path string, edit func(map[string]any)) error {
	raw := map[string]any{}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return err
	}
	edit(raw)
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(raw); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func section(raw map[string]any, key string) map[string]any {
	if m, ok := raw[key].(map[string]any); ok {
		return m
	}
	m := map[string]any{}
	raw[key] = m
	return m
}
