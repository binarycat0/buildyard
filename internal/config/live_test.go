package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiveSetLimitsKeepsOtherKeys(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte("port = 7790\nfuture = \"kept\"\n[retention]\nmaxLogBytes = \"1GB\"\n[defaults]\nflags = [\"-ntp\"]\n"), 0o600)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	l := NewLive(p, cfg)
	var seen []Config
	l.OnChange(func(c Config) { seen = append(seen, c) })

	if err := l.SetLimits(Limits{MaxConcurrentJobs: 3, JobsPerProject: 50}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0].MaxConcurrentJobs != 3 || seen[0].Retention.JobsPerProject != 50 {
		t.Fatalf("hook got %+v", seen)
	}
	again, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if again.Port != 7790 || again.MaxConcurrentJobs != 3 || again.Retention.JobsPerProject != 50 || again.Retention.MaxLogBytes != "1GB" {
		t.Fatalf("file after save: %+v", again)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `future = "kept"`) {
		t.Errorf("unknown key lost:\n%s", b)
	}
}

func TestLiveSetLimitsRejectsBadValues(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	l := NewLive(p, Default())
	for _, v := range []Limits{{MaxConcurrentJobs: 0, JobsPerProject: 10}, {MaxConcurrentJobs: 1, JobsPerProject: -1}} {
		if err := l.SetLimits(v); err == nil {
			t.Errorf("SetLimits(%+v) = nil, want an error", v)
		}
	}
	if _, err := os.Stat(p); err == nil {
		t.Error("a rejected change wrote the file")
	}
	if l.Get().MaxConcurrentJobs != 1 {
		t.Error("a rejected change was applied")
	}
}

func TestLiveReloadKeepsPort(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	l := NewLive(p, Default())
	os.WriteFile(p, []byte("port = 9999\nmaxConcurrentJobs = 4\n"), 0o600)
	if err := l.Reload(); err != nil {
		t.Fatal(err)
	}
	if c := l.Get(); c.Port != DefaultPort || c.MaxConcurrentJobs != 4 {
		t.Errorf("after reload: %+v", c)
	}
}
