// Package config resolves the Buildyard data directory and loads config.toml.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// DefaultPort is the port the daemon listens on when config.toml does not set one.
const DefaultPort = 7777

// Config mirrors ~/.buildyard/config.toml.
type Config struct {
	Port              int       `toml:"port"`
	MaxConcurrentJobs int       `toml:"maxConcurrentJobs"`
	Retention         Retention `toml:"retention"`
	Defaults          Defaults  `toml:"defaults"`
	Telemetry         Telemetry `toml:"telemetry"`
}

// Telemetry is the optional OpenTelemetry export of builds: each job as a trace
// (startup, modules, goals, downloads) and its output as log records, sent over
// OTLP/HTTP to a collector such as grafana/otel-lgtm or Grafana Alloy.
type Telemetry struct {
	Enabled     bool              `toml:"enabled" json:"enabled"`
	Endpoint    string            `toml:"endpoint" json:"endpoint"` // OTLP/HTTP base URL; /v1/traces and /v1/logs are appended
	Traces      bool              `toml:"traces" json:"traces"`
	Logs        bool              `toml:"logs" json:"logs"`
	ParseLogs   bool              `toml:"parseLogs" json:"parseLogs"` // levels, module/goal, multi-line records
	ServiceName string            `toml:"serviceName" json:"serviceName"`
	Headers     map[string]string `toml:"headers" json:"headers"`       // e.g. Authorization for a hosted backend
	GrafanaURL  string            `toml:"grafanaUrl" json:"grafanaUrl"` // for "open trace" links; empty = no links
}

// DefaultTelemetry is the telemetry config before the user changes it: off, and
// pointing at a collector on this machine.
func DefaultTelemetry() Telemetry {
	return Telemetry{Endpoint: "http://localhost:4318", Traces: true, Logs: true, ParseLogs: true, ServiceName: "buildyard", GrafanaURL: "http://localhost:3000"}
}

// Validate checks the telemetry settings; endpoints only matter when export is on.
func (t Telemetry) Validate() error {
	check := func(name, raw string, required bool) error {
		if raw == "" {
			if required {
				return fmt.Errorf("%s is required", name)
			}
			return nil
		}
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%s must be an http(s) URL, got %q", name, raw)
		}
		return nil
	}
	if err := check("telemetry endpoint", t.Endpoint, t.Enabled); err != nil {
		return err
	}
	return check("Grafana URL", t.GrafanaURL, false)
}

type Retention struct {
	JobsPerProject int    `toml:"jobsPerProject"`
	MaxLogBytes    string `toml:"maxLogBytes"`
}

type Defaults struct {
	Flags []string `toml:"flags"`
}

// Default returns the configuration used when config.toml is missing or leaves fields unset.
func Default() Config {
	return Config{
		Port:              DefaultPort,
		MaxConcurrentJobs: 1,
		Retention:         Retention{JobsPerProject: 200, MaxLogBytes: "2GB"},
		Defaults:          Defaults{Flags: []string{"-ntp"}},
		Telemetry:         DefaultTelemetry(),
	}
}

// Paths are the files and directories under the data directory.
type Paths struct {
	Dir    string
	Config string
	Lock   string
	Port   string // the running daemon's port, for the CLI
	Log    string
	DB     string
	Logs   string
	Events string
	Ext    string
	Cache  string
}

// DataDir returns $BUILDYARD_HOME if set, otherwise ~/.buildyard.
func DataDir() (string, error) {
	if d := os.Getenv("BUILDYARD_HOME"); d != "" {
		return filepath.Abs(d)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home directory: %w", err)
	}
	return filepath.Join(home, ".buildyard"), nil
}

// PathsIn lays out the data directory rooted at dir.
func PathsIn(dir string) Paths {
	return Paths{
		Dir:    dir,
		Config: filepath.Join(dir, "config.toml"),
		Lock:   filepath.Join(dir, "daemon.lock"),
		Port:   filepath.Join(dir, "daemon.port"),
		Log:    filepath.Join(dir, "daemon.log"),
		DB:     filepath.Join(dir, "buildyard.db"),
		Logs:   filepath.Join(dir, "logs"),
		Events: filepath.Join(dir, "events"),
		Ext:    filepath.Join(dir, "ext"),
		Cache:  filepath.Join(dir, "cache"),
	}
}

// Ensure creates the data directory and its subdirectories.
func (p Paths) Ensure() error {
	for _, d := range []string{p.Dir, p.Logs, p.Events, p.Ext, p.Cache} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// Load reads config.toml, filling unset fields with defaults. A missing file is not an error.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	var file Config
	md, err := toml.Decode(string(data), &file)
	if err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	if md.IsDefined("port") {
		cfg.Port = file.Port
	}
	if md.IsDefined("maxConcurrentJobs") {
		cfg.MaxConcurrentJobs = file.MaxConcurrentJobs
	}
	if md.IsDefined("retention", "jobsPerProject") {
		cfg.Retention.JobsPerProject = file.Retention.JobsPerProject
	}
	if md.IsDefined("retention", "maxLogBytes") {
		cfg.Retention.MaxLogBytes = file.Retention.MaxLogBytes
	}
	if md.IsDefined("defaults", "flags") {
		cfg.Defaults.Flags = file.Defaults.Flags
	}
	t := &cfg.Telemetry
	if md.IsDefined("telemetry", "enabled") {
		t.Enabled = file.Telemetry.Enabled
	}
	if md.IsDefined("telemetry", "endpoint") {
		t.Endpoint = file.Telemetry.Endpoint
	}
	if md.IsDefined("telemetry", "traces") {
		t.Traces = file.Telemetry.Traces
	}
	if md.IsDefined("telemetry", "logs") {
		t.Logs = file.Telemetry.Logs
	}
	if md.IsDefined("telemetry", "parseLogs") {
		t.ParseLogs = file.Telemetry.ParseLogs
	}
	if md.IsDefined("telemetry", "serviceName") {
		t.ServiceName = file.Telemetry.ServiceName
	}
	if md.IsDefined("telemetry", "headers") {
		t.Headers = file.Telemetry.Headers
	}
	if md.IsDefined("telemetry", "grafanaUrl") {
		t.GrafanaURL = file.Telemetry.GrafanaURL
	}
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Validate checks values that would make the daemon misbehave.
func (c Config) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("port %d out of range", c.Port)
	}
	if c.MaxConcurrentJobs < 1 {
		return fmt.Errorf("maxConcurrentJobs must be >= 1")
	}
	return c.Telemetry.Validate()
}
