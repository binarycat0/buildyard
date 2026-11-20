package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileGivesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != DefaultPort || cfg.MaxConcurrentJobs != 1 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadPartialFileKeepsOtherDefaults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte("port = 8080\n[defaults]\nflags = [\"-ntp\", \"-e\"]\n"), 0o600)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 8080 || len(cfg.Defaults.Flags) != 2 {
		t.Fatalf("file values not applied: %+v", cfg)
	}
	if cfg.MaxConcurrentJobs != 1 || cfg.Retention.JobsPerProject != 200 {
		t.Fatalf("defaults lost: %+v", cfg)
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	cases := map[string]string{
		"port":   "port = 70000\n",
		"jobs":   "maxConcurrentJobs = 0\n",
		"syntax": "port = \n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.toml")
			os.WriteFile(p, []byte(body), 0o600)
			if _, err := Load(p); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestTelemetryConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg, err := Load(path)
	if err != nil || cfg.Telemetry.Enabled || cfg.Telemetry.Endpoint != "http://localhost:4318" || !cfg.Telemetry.Logs {
		t.Fatalf("defaults: %+v %v", cfg.Telemetry, err)
	}
	live := NewLive(path, cfg)
	if err := live.SetTelemetry(Telemetry{Enabled: true, Endpoint: "ftp://x"}); err == nil {
		t.Fatal("a non-http endpoint must be rejected")
	}
	if err := live.SetTelemetry(Telemetry{Enabled: true, Endpoint: "http://collector:4318/", Traces: true}); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	tl := got.Telemetry
	if !tl.Enabled || tl.Endpoint != "http://collector:4318" || !tl.Traces || tl.Logs || tl.ServiceName != "buildyard" {
		t.Fatalf("reloaded: %+v", tl)
	}
}
