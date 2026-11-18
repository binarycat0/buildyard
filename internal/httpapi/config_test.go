package httpapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/binarycat0/buildyard/internal/config"
)

func TestConfigAPI(t *testing.T) {
	s, _ := newTestServer(t, t.TempDir())
	path := filepath.Join(t.TempDir(), "config.toml")
	s.Config = config.NewLive(path, config.Default())
	var applied []config.Config
	s.Config.OnChange(func(c config.Config) { applied = append(applied, c) })
	h := s.Handler()

	w := do(t, h, req{method: "GET", path: "/api/config"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"maxConcurrentJobs":1`) || !strings.Contains(w.Body.String(), `"port":7777`) {
		t.Fatalf("get: %d %s", w.Code, w.Body)
	}
	w = do(t, h, req{method: "PUT", path: "/api/config", body: `{"maxConcurrentJobs":3,"jobsPerProject":20}`})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"maxConcurrentJobs":3`) {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	if len(applied) != 1 || applied[0].MaxConcurrentJobs != 3 {
		t.Errorf("not applied: %+v", applied)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "maxConcurrentJobs = 3") {
		t.Errorf("not saved:\n%s", b)
	}
	if w = do(t, h, req{method: "PUT", path: "/api/config", body: `{"maxConcurrentJobs":0,"jobsPerProject":20}`}); w.Code != 400 {
		t.Errorf("bad value: %d %s", w.Code, w.Body)
	}
}
