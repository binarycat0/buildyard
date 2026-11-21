package telemetry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/binarycat0/buildyard/internal/config"
	"github.com/binarycat0/buildyard/internal/store"
)

type collector struct {
	mu    sync.Mutex
	spans []map[string]any
	logs  []map[string]any
	hdr   string
}

func (c *collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var doc map[string]any
	json.Unmarshal(body, &doc)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hdr = r.Header.Get("X-Test")
	pick := func(top, scope, items string) {
		for _, rs := range doc[top].([]any) {
			for _, ss := range rs.(map[string]any)[scope].([]any) {
				for _, it := range ss.(map[string]any)[items].([]any) {
					if items == "spans" {
						c.spans = append(c.spans, it.(map[string]any))
					} else {
						c.logs = append(c.logs, it.(map[string]any))
					}
				}
			}
		}
	}
	switch r.URL.Path {
	case "/v1/traces":
		pick("resourceSpans", "scopeSpans", "spans")
	case "/v1/logs":
		pick("resourceLogs", "scopeLogs", "logRecords")
	default:
		http.NotFound(w, r)
		return
	}
	w.Write([]byte("{}"))
}

func TestJobExport(t *testing.T) {
	col := &collector{}
	srv := httptest.NewServer(col)
	defer srv.Close()
	cfg := config.DefaultTelemetry()
	cfg.Enabled, cfg.Endpoint, cfg.Headers = true, srv.URL, map[string]string{"X-Test": "yes"}
	e := New(cfg, "test", nil)

	j := e.StartJob(store.Job{ID: 7}, Labels{Project: "demo", Task: "fast"}, []string{"mvn", "install"})
	for _, ev := range []string{
		`{"t":"download","kind":"artifact","name":"g:parent:pom:1","repo":"central","ok":true,"bytes":10,"ms":5,"ts":1000}`,
		`{"t":"session","ts":1100,"projects":[{"key":"g:a","name":"a"},{"key":"g:b","name":"b"}]}`,
		`{"t":"project_start","key":"g:a","ts":1200}`,
		`{"t":"download","key":"g:a","kind":"artifact","name":"g:dep:jar:1","repo":"central","ok":true,"bytes":100,"ms":50,"ts":1260}`,
		`{"t":"mojo_start","key":"g:a","mojo":"maven-compiler-plugin:compile","id":"default-compile","ts":1300}`,
		`{"t":"mojo_end","key":"g:a","mojo":"maven-compiler-plugin:compile","id":"default-compile","status":"failed","ms":100,"ts":1400}`,
		`{"t":"project_end","key":"g:a","status":"failed","ms":200,"ts":1400}`,
		`{"t":"project_start","key":"g:b","ts":1410}`,
	} {
		j.Event([]byte(ev))
	}
	j.Line(1, "\x1b[1;31m[ERROR]\x1b[m boom")
	exit := 1
	j.End(store.JobFailed, &exit)
	e.flush(context.Background())

	byName := map[string]map[string]any{}
	for _, s := range col.spans {
		byName[s["name"].(string)] = s
	}
	for _, n := range []string{"demo / fast", "startup", "a", "b", "maven-compiler-plugin:compile (default-compile)", "download g:parent:pom:1", "download g:dep:jar:1"} {
		if byName[n] == nil {
			t.Fatalf("no span %q in %v", n, len(col.spans))
		}
	}
	root := byName["demo / fast"]
	parent := func(n string) string { return byName[n]["parentSpanId"].(string) }
	if parent("startup") != root["spanId"] || parent("a") != root["spanId"] {
		t.Error("startup and modules must hang off the root span")
	}
	if parent("download g:parent:pom:1") != byName["startup"]["spanId"] {
		t.Error("a download before the reactor belongs to startup")
	}
	if parent("download g:dep:jar:1") != byName["a"]["spanId"] || parent("maven-compiler-plugin:compile (default-compile)") != byName["a"]["spanId"] {
		t.Error("goals and downloads of a module belong to its span")
	}
	if root["status"] == nil || byName["a"]["status"] == nil {
		t.Error("failed job and module must have an error status")
	}
	if len(col.logs) != 1 || col.logs[0]["severityText"] != nil || body(col.logs[0]) != "[ERROR] boom" {
		t.Errorf("log record: %v", col.logs)
	}
	if col.logs[0]["traceId"] != root["traceId"] {
		t.Error("log records must point at the job's trace")
	}
	if col.hdr != "yes" {
		t.Error("custom headers not sent")
	}
	if st := e.Status(); st.SentSpans != int64(len(col.spans)) || st.SentLogs != 1 || st.LastError != "" {
		t.Errorf("status %+v", st)
	}
}

func TestDisabled(t *testing.T) {
	e := New(config.DefaultTelemetry(), "test", nil)
	if j := e.StartJob(store.Job{ID: 1}, Labels{}, nil); j != nil {
		t.Fatal("export is off by default")
	}
}

func TestSendErrorIsCounted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadRequest)
	}))
	defer srv.Close()
	cfg := config.DefaultTelemetry()
	cfg.Enabled, cfg.Endpoint = true, srv.URL
	e := New(cfg, "test", nil)
	j := e.StartJob(store.Job{ID: 1}, Labels{Project: "p"}, nil)
	j.Line(1, "x")
	e.flush(context.Background())
	st := e.Status()
	if st.DroppedLogs != 1 || st.LastError == "" {
		t.Fatalf("status %+v", st)
	}
	if err := e.Test(context.Background(), cfg); err == nil {
		t.Fatal("Test must report the collector's error")
	}
}

func body(r map[string]any) string { return r["body"].(map[string]any)["stringValue"].(string) }

func attr(r map[string]any, key string) string {
	for _, a := range r["attributes"].([]any) {
		m := a.(map[string]any)
		if m["key"] == key {
			for _, v := range m["value"].(map[string]any) {
				return v.(string)
			}
		}
	}
	return ""
}

func logJob(t *testing.T) (*Exporter, *Job, *collector, func()) {
	col := &collector{}
	srv := httptest.NewServer(col)
	cfg := config.DefaultTelemetry()
	cfg.Enabled, cfg.Endpoint, cfg.Traces = true, srv.URL, false
	e := New(cfg, "test", nil)
	return e, e.StartJob(store.Job{ID: 1}, Labels{Project: "p"}, nil), col, srv.Close
}

// Lines go out as they are, one record each; only the run's context is added.
func TestLogLinesAreSentRaw(t *testing.T) {
	e, j, col, done := logJob(t)
	defer done()
	j.Event([]byte(`{"t":"session","ts":1,"projects":[{"key":"demo:a","name":"A"}]}`))
	lines := []string{
		"[INFO] -------------------------< demo:a >-------------------------",
		"[INFO] --- compiler:3.13.0:compile (default-compile) @ a ---",
		"[ERROR] Failed to run",
		"\tat x.Y.z(Y.java:1)",
		"[INFO] BUILD FAILURE",
		"\x1b[1mbold\x1b[0m",
	}
	for i, l := range lines {
		j.Line(i+1, l)
	}
	j.End(store.JobFailed, nil)
	e.flush(context.Background())
	if len(col.logs) != len(lines) {
		t.Fatalf("got %d records, want %d", len(col.logs), len(lines))
	}
	errRec := col.logs[2]
	if body(errRec) != "[ERROR] Failed to run" || errRec["severityNumber"] != nil || attr(errRec, "buildyard.log.line") != "3" {
		t.Errorf("error line: %v", errRec)
	}
	if attr(errRec, "maven.module") != "demo:a" || attr(errRec, "maven.goal") != "compiler:compile (default-compile)" {
		t.Errorf("module/goal: %v", errRec["attributes"])
	}
	if body(col.logs[3]) != "\tat x.Y.z(Y.java:1)" || attr(col.logs[3], "maven.module") != "demo:a" {
		t.Errorf("stack frame: %v", col.logs[3])
	}
	if attr(col.logs[4], "maven.module") != "" {
		t.Error("the build summary is about no module")
	}
	if body(col.logs[5]) != "bold" {
		t.Errorf("ANSI codes are removed: %q", body(col.logs[5]))
	}
	if col.logs[0]["traceId"] == "" {
		t.Error("records point at the job's trace")
	}
}
