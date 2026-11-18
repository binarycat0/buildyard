package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/binarycat0/buildyard/internal/jdk"
	"github.com/binarycat0/buildyard/internal/runner"
	"github.com/binarycat0/buildyard/internal/store"
	"github.com/binarycat0/buildyard/internal/tasks"
)

func newServer(t *testing.T) (http.Handler, string) {
	t.Helper()
	return newServerHome(t, t.TempDir())
}

// newServerHome uses userHome as the home directory for JDK and mvn/mvnd lookup.
func newServerHome(t *testing.T, userHome string) (http.Handler, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "buildyard.db")
	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ui := fstest.MapFS{
		"index.html": {Data: []byte("<html>buildyard</html>")},
		"app.js":     {Data: []byte("console.log(1)")},
	}
	bg := tasks.New(1, nil)
	t.Cleanup(bg.Close)
	s := &Server{Store: st, Tasks: bg, JDKs: jdk.Detector{Home: userHome, GOOS: "darwin", SystemRoot: t.TempDir(),
		Run:      func(string, ...string) ([]byte, error) { return nil, os.ErrNotExist },
		Getenv:   func(string) string { return "" },
		LookPath: func(string) string { return "" }}, Home: userHome, DataDir: "/tmp/x", UI: ui}
	s.Runner = runner.New(st, t.TempDir(), 1, 0, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { s.Runner.Shutdown(context.Background()) })
	return s.Handler(), dbPath
}

type req struct {
	method, path, body string
	header             map[string]string
}

func do(t *testing.T, h http.Handler, r req) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if r.body != "" {
		body = strings.NewReader(r.body)
	}
	hr := httptest.NewRequest(r.method, r.path, body)
	hr.Host = "127.0.0.1:7777"
	for k, v := range r.header {
		hr.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, hr)
	return w
}

func TestUIServesFilesAndFallsBack(t *testing.T) {
	h, _ := newServer(t)
	if w := do(t, h, req{method: "GET", path: "/app.js"}); w.Code != 200 || !strings.Contains(w.Body.String(), "console") {
		t.Fatalf("app.js: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, req{method: "GET", path: "/projects/1"}); w.Code != 200 || !strings.Contains(w.Body.String(), "buildyard") {
		t.Fatalf("fallback: %d %s", w.Code, w.Body)
	}
}

func TestProjectsAPI(t *testing.T) {
	h, dbPath := newServer(t)
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "pom.xml"), []byte("<project/>"), 0o644)
	noPom := t.TempDir()

	call := func(method, path, body string) *httptest.ResponseRecorder {
		return do(t, h, req{method: method, path: path, body: body})
	}
	mustJSON := func(w *httptest.ResponseRecorder, v any) {
		t.Helper()
		if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
			t.Fatalf("decode %s: %v", w.Body, err)
		}
	}

	w := call("POST", "/api/projects", `{"rootPath":"`+root+`"}`)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var p store.Project
	mustJSON(w, &p)
	if p.Name != filepath.Base(p.RootPath) || p.BuildTool != "maven" {
		t.Fatalf("created %+v", p)
	}

	for name, tc := range map[string]struct {
		body, code string
		status     int
	}{
		"duplicate":    {`{"rootPath":"` + root + `"}`, "duplicate_root", 409},
		"no pom":       {`{"rootPath":"` + noPom + `"}`, "no_build_file", 400},
		"relative":     {`{"rootPath":"src/app"}`, "bad_path", 400},
		"unknown":      {`{"rootPath":"` + root + `","x":1}`, "bad_request", 400},
		"invalid json": {`{`, "bad_request", 400},
	} {
		t.Run(name, func(t *testing.T) {
			w := call("POST", "/api/projects", tc.body)
			var e apiError
			mustJSON(w, &e)
			if w.Code != tc.status || e.Error != tc.code {
				t.Fatalf("got %d %+v", w.Code, e)
			}
		})
	}

	id := "/api/projects/" + strconv.FormatInt(p.ID, 10)
	w = call("PATCH", id, `{"name":"Renamed","executable":"wrapper","env":{"MAVEN_OPTS":"-Xmx8g"}}`)
	mustJSON(w, &p)
	if w.Code != 200 || p.Name != "Renamed" || *p.Executable != "wrapper" || p.Env["MAVEN_OPTS"] != "-Xmx8g" {
		t.Fatalf("patch: %d %s", w.Code, w.Body)
	}
	if w := call("PATCH", id, `{"name":"  "}`); w.Code != 400 {
		t.Fatalf("empty name: %d", w.Code)
	}
	if w := call("PATCH", id, `{"executable":"./mvnw"}`); w.Code != 400 {
		t.Fatalf("relative executable: %d", w.Code)
	}
	if w := call("PATCH", id, `{"jdkId":999}`); w.Code != 400 {
		t.Fatalf("unknown jdk: %d", w.Code)
	}
	if w := call("PATCH", id, `{"skipPresets":{"a":1}}`); w.Code != 400 {
		t.Fatalf("object skipPresets: %d", w.Code)
	}

	var list []store.Project
	w = call("GET", "/api/projects", "")
	mustJSON(w, &list)
	if len(list) != 1 || list[0].Name != "Renamed" {
		t.Fatalf("list: %s", w.Body)
	}

	// Restart: a new server on the same database still has the project.
	st2, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	h2 := (&Server{Store: st2, Tasks: tasks.New(1, nil), UI: fstest.MapFS{"index.html": {}}}).Handler()
	w = do(t, h2, req{method: "GET", path: id})
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte("Renamed")) {
		t.Fatalf("after restart: %d %s", w.Code, w.Body)
	}

	if w := call("DELETE", id, ""); w.Code != 204 {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := call("GET", id, ""); w.Code != 404 {
		t.Fatalf("get deleted: %d", w.Code)
	}
	if w := call("GET", "/api/projects/abc", ""); w.Code != 400 {
		t.Fatalf("bad id: %d", w.Code)
	}
}
