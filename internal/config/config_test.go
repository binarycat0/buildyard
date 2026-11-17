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
