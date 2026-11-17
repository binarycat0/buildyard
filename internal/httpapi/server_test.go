package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/binarycat0/buildyard/internal/store"
)

const testPort = 7777
const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func newServer(t *testing.T) (http.Handler, string) {
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
	s := &Server{Store: st, Token: testToken, Port: testPort, DataDir: "/tmp/x", UI: ui}
	return s.Handler(), dbPath
}

type req struct {
	method, path, body string
	host               string // default 127.0.0.1:7777
	header             map[string]string
	cookie             bool
}

func do(t *testing.T, h http.Handler, r req) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if r.body != "" {
		body = strings.NewReader(r.body)
	}
	hr := httptest.NewRequest(r.method, r.path, body)
	hr.Host = "127.0.0.1:7777"
	if r.host != "" {
		hr.Host = r.host
	}
	for k, v := range r.header {
		hr.Header.Set(k, v)
	}
	if r.cookie {
		hr.AddCookie(&http.Cookie{Name: CookieName, Value: testToken})
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, hr)
	return w
}

var tokenHeader = map[string]string{HeaderName: testToken}

func TestSecurity(t *testing.T) {
	h, _ := newServer(t)
	origin := map[string]string{"Origin": "http://127.0.0.1:7777"}
	evil := map[string]string{"Origin": "https://evil.example"}

	tests := []struct {
		name string
		r    req
		want int
	}{
		{"no token api", req{method: "GET", path: "/api/projects"}, 401},
		{"no token ui", req{method: "GET", path: "/"}, 401},
		{"wrong token", req{method: "GET", path: "/api/projects", header: map[string]string{HeaderName: "nope"}}, 401},
		{"header token", req{method: "GET", path: "/api/projects", header: tokenHeader}, 200},
		{"cookie token", req{method: "GET", path: "/api/projects", cookie: true}, 200},
		{"localhost host", req{method: "GET", path: "/api/health", host: "localhost:7777", cookie: true}, 200},
		{"foreign host", req{method: "GET", path: "/api/health", host: "evil.example:7777", cookie: true}, 403},
		{"rebinding host wrong port", req{method: "GET", path: "/api/health", host: "127.0.0.1:80", cookie: true}, 403},
		{"foreign host before auth", req{method: "GET", path: "/api/health", host: "evil.example"}, 403},
		{"cookie post with origin", req{method: "POST", path: "/api/projects", body: `{}`, cookie: true, header: origin}, 400},
		{"cookie post without origin", req{method: "POST", path: "/api/projects", body: `{}`, cookie: true}, 403},
		{"cookie post foreign origin", req{method: "POST", path: "/api/projects", body: `{}`, cookie: true, header: evil}, 403},
		{"header post without origin (cli)", req{method: "POST", path: "/api/projects", body: `{}`, header: tokenHeader}, 400},
		{"header post foreign origin", req{method: "DELETE", path: "/api/projects/1", header: map[string]string{HeaderName: testToken, "Origin": "https://evil.example"}}, 403},
		{"unknown api", req{method: "GET", path: "/api/nope", cookie: true}, 404},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if w := do(t, h, tt.r); w.Code != tt.want {
				t.Fatalf("status %d, want %d: %s", w.Code, tt.want, w.Body)
			}
		})
	}
}

func TestTokenLinkSetsCookieAndRedirects(t *testing.T) {
	h, _ := newServer(t)
	w := do(t, h, req{method: "GET", path: "/?token=" + testToken + "&tab=x"})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/?tab=x" {
		t.Fatalf("location %q", loc)
	}
	c := w.Result().Cookies()
	if len(c) != 1 || c[0].Value != testToken || !c[0].HttpOnly || c[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie %+v", c)
	}
	if w := do(t, h, req{method: "GET", path: "/?token=bad"}); w.Code != 401 {
		t.Fatalf("bad token link: %d", w.Code)
	}
}

func TestUIServesFilesAndFallsBack(t *testing.T) {
	h, _ := newServer(t)
	if w := do(t, h, req{method: "GET", path: "/app.js", cookie: true}); w.Code != 200 || !strings.Contains(w.Body.String(), "console") {
		t.Fatalf("app.js: %d %s", w.Code, w.Body)
	}
	if w := do(t, h, req{method: "GET", path: "/projects/1", cookie: true}); w.Code != 200 || !strings.Contains(w.Body.String(), "buildyard") {
		t.Fatalf("fallback: %d %s", w.Code, w.Body)
	}
}

func TestProjectsAPI(t *testing.T) {
	h, dbPath := newServer(t)
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "pom.xml"), []byte("<project/>"), 0o644)
	noPom := t.TempDir()

	call := func(method, path, body string) *httptest.ResponseRecorder {
		return do(t, h, req{method: method, path: path, body: body, header: tokenHeader})
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

	id := "/api/projects/" + itoa(int(p.ID))
	w = call("PATCH", id, `{"name":"Renamed","executable":"./mvnw","env":{"MAVEN_OPTS":"-Xmx8g"}}`)
	mustJSON(w, &p)
	if w.Code != 200 || p.Name != "Renamed" || *p.Executable != "./mvnw" || p.Env["MAVEN_OPTS"] != "-Xmx8g" {
		t.Fatalf("patch: %d %s", w.Code, w.Body)
	}
	if w := call("PATCH", id, `{"name":"  "}`); w.Code != 400 {
		t.Fatalf("empty name: %d", w.Code)
	}
	if w := call("PATCH", id, `{"javaHome":"jdk21"}`); w.Code != 400 {
		t.Fatalf("relative javaHome: %d", w.Code)
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
	h2 := (&Server{Store: st2, Token: testToken, Port: testPort, UI: fstest.MapFS{"index.html": {}}}).Handler()
	w = do(t, h2, req{method: "GET", path: id, header: tokenHeader})
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
