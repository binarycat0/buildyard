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
	if cfg.Port != DefaultPort || cfg.MaxConcurrentJobs != 1 || cfg.Defaults.Executable != "auto" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadPartialFileKeepsOtherDefaults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte("port = 8080\n[defaults]\nexecutable = \"mvnw\"\n"), 0o600)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 8080 || cfg.Defaults.Executable != "mvnw" {
		t.Fatalf("file values not applied: %+v", cfg)
	}
	if cfg.MaxConcurrentJobs != 1 || len(cfg.Defaults.Flags) != 1 {
		t.Fatalf("defaults lost: %+v", cfg)
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	cases := map[string]string{
		"port":       "port = 70000\n",
		"jobs":       "maxConcurrentJobs = 0\n",
		"executable": "[defaults]\nexecutable = \"gradle\"\n",
		"syntax":     "port = \n",
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

func TestTokenCreatedOnceWithMode0600(t *testing.T) {
	p := filepath.Join(t.TempDir(), "token")
	a, err := LoadOrCreateToken(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 64 {
		t.Fatalf("token length %d", len(a))
	}
	b, err := LoadOrCreateToken(p)
	if err != nil || a != b {
		t.Fatalf("token changed on second load: %v", err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
}
