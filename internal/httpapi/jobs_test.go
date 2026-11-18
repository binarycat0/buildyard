package httpapi

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/binarycat0/buildyard/internal/store"
)

// mavenProject makes a project whose ./mvnw prints its arguments and environment.
func mavenProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "pom.xml"), []byte("<project/>"), 0o644)
	os.WriteFile(filepath.Join(root, "mvnw"), []byte(`#!/bin/sh
echo "args: $*"
echo "foo: $FOO"
case "$*" in *slow*) i=0; while [ $i -lt 30 ]; do echo tick$i; i=$((i+1)); sleep 0.05; done ;; esac
case "$*" in *fail*) echo "[ERROR] boom"; exit 2 ;; esac
echo "BUILD SUCCESS"
`), 0o755)
	return root
}

func waitJob(t *testing.T, h http.Handler, id int64) jobView {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var j jobView
		decodeInto(do(t, h, req{method: "GET", path: "/api/jobs/" + strconv.FormatInt(id, 10)}), &j)
		if j.Finished() {
			return j
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %d did not finish", id)
	return jobView{}
}

func TestRunJobPreviewLogRerun(t *testing.T) {
	h, _ := newServer(t)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		return do(t, h, req{method: method, path: path, body: body})
	}
	root := mavenProject(t)
	var p store.Project
	decodeInto(call("POST", "/api/projects", `{"rootPath":"`+root+`"}`), &p)
	pid := strconv.FormatInt(p.ID, 10)
	call("PATCH", "/api/projects/"+pid, `{"env":{"FOO":"bar"}}`)

	var pv preview
	decodeInto(call("POST", "/api/projects/"+pid+"/preview", `{"spec":{"goals":["clean"," install "],"extraArgs":["-DskipTests",""]}}`), &pv)
	want := []string{filepath.Join(root, "mvnw"), "-B", "clean", "install", "-DskipTests"}
	if strings.Join(pv.Argv, " ") != strings.Join(want, " ") || pv.Error != "" || pv.Dir != root {
		t.Fatalf("preview: %+v", pv)
	}
	decodeInto(call("POST", "/api/projects/"+pid+"/preview", `{"spec":{"goals":[]}}`), &pv)
	if pv.Error == "" {
		t.Fatal("no goals should be an error")
	}
	if w := call("POST", "/api/projects/"+pid+"/jobs", `{"spec":{"goals":["-X"]}}`); w.Code != 400 {
		t.Fatalf("option as goal: %d", w.Code)
	}

	w := call("POST", "/api/projects/"+pid+"/jobs", `{"spec":{"goals":["install"],"extraArgs":["-DskipTests"]}}`)
	if w.Code != 201 {
		t.Fatalf("run: %d %s", w.Code, w.Body)
	}
	var j jobView
	decodeInto(w, &j)
	done := waitJob(t, h, j.ID)
	if done.Status != store.JobSucceeded || done.LogLines != 3 || done.LiveLines != 3 {
		t.Fatalf("job: %+v", done)
	}
	var page logPage
	decodeInto(call("GET", "/api/jobs/"+strconv.FormatInt(j.ID, 10)+"/log", ""), &page)
	if page.Total != 3 || page.Lines[0] != "args: -B install -DskipTests" || page.Lines[1] != "foo: bar" {
		t.Fatalf("log: %+v", page)
	}

	// Rerun keeps the spec; a failing build is "failed".
	decodeInto(call("POST", "/api/projects/"+pid+"/jobs", `{"spec":{"goals":["fail"]}}`), &j)
	if got := waitJob(t, h, j.ID); got.Status != store.JobFailed || *got.ExitCode != 2 {
		t.Fatalf("fail: %+v", got)
	}
	w = call("POST", "/api/jobs/"+strconv.FormatInt(j.ID, 10)+"/rerun", "")
	var again jobView
	decodeInto(w, &again)
	if w.Code != 201 || again.ID == j.ID || !strings.Contains(string(again.Spec), `"fail"`) {
		t.Fatalf("rerun: %d %s", w.Code, w.Body)
	}
	waitJob(t, h, again.ID)

	var list []jobView
	decodeInto(call("GET", "/api/projects/"+pid+"/jobs", ""), &list)
	if len(list) != 3 || list[0].ID != again.ID {
		t.Fatalf("list: %d", len(list))
	}
	var proj store.Project
	decodeInto(call("GET", "/api/projects/"+pid, ""), &proj)
	if proj.LastJob == nil || proj.LastJob.ID != again.ID {
		t.Fatalf("last job: %+v", proj.LastJob)
	}
	if w := call("POST", "/api/jobs/"+strconv.FormatInt(again.ID, 10)+"/cancel", ""); w.Code != 409 {
		t.Fatalf("cancel finished: %d", w.Code)
	}
}

type sseEvent struct{ event, id, data string }

func readSSE(t *testing.T, url, lastID string) []sseEvent {
	t.Helper()
	rq, _ := http.NewRequest("GET", url, nil)
	if lastID != "" {
		rq.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(rq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out []sseEvent
	var cur sseEvent
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			out = append(out, cur)
			if cur.event == "end" {
				return out
			}
			cur = sseEvent{}
		case strings.HasPrefix(line, "event: "):
			cur.event = line[7:]
		case strings.HasPrefix(line, "id: "):
			cur.id = line[4:]
		case strings.HasPrefix(line, "data: "):
			cur.data = line[6:]
		}
	}
	return out
}

// A browser that opens a job mid-run gets the whole log, then the live tail and an end event.
func TestJobEventsStream(t *testing.T) {
	h, _ := newServer(t)
	srv := httptest.NewServer(h)
	defer srv.Close()
	root := mavenProject(t)
	var p store.Project
	decodeInto(do(t, h, req{method: "POST", path: "/api/projects", body: `{"rootPath":"` + root + `"}`}), &p)
	var j jobView
	decodeInto(do(t, h, req{method: "POST", path: "/api/projects/" + strconv.FormatInt(p.ID, 10) + "/jobs",
		body: `{"spec":{"goals":["slow"]}}`}), &j)
	base := srv.URL + "/api/jobs/" + strconv.FormatInt(j.ID, 10) + "/stream"

	time.Sleep(400 * time.Millisecond) // mid-run
	evs := readSSE(t, base, "")
	var lines []string
	for _, e := range evs {
		if e.event == "log" {
			var le struct {
				N    int
				Text string
			}
			json.Unmarshal([]byte(e.data), &le)
			if le.N != len(lines)+1 || e.id != strconv.Itoa(le.N) {
				t.Fatalf("line %d (id %s) after %d", le.N, e.id, len(lines))
			}
			lines = append(lines, le.Text)
		}
	}
	if len(lines) != 33 || lines[32] != "BUILD SUCCESS" || evs[len(evs)-1].event != "end" {
		t.Fatalf("got %d lines, last event %+v", len(lines), evs[len(evs)-1])
	}

	// Reconnect after the end with Last-Event-ID: only what came after, then end again.
	evs = readSSE(t, base, "30")
	n := 0
	for _, e := range evs {
		if e.event == "log" {
			n++
		}
	}
	if n != 3 || evs[len(evs)-1].event != "end" || !strings.Contains(evs[len(evs)-1].data, `"succeeded"`) {
		t.Fatalf("resume: %d lines %+v", n, evs)
	}
}

func TestCancelJobAPI(t *testing.T) {
	h, _ := newServer(t)
	root := mavenProject(t)
	var p store.Project
	decodeInto(do(t, h, req{method: "POST", path: "/api/projects", body: `{"rootPath":"` + root + `"}`}), &p)
	var j jobView
	decodeInto(do(t, h, req{method: "POST", path: "/api/projects/" + strconv.FormatInt(p.ID, 10) + "/jobs",
		body: `{"spec":{"goals":["slow"]}}`}), &j)
	time.Sleep(200 * time.Millisecond)
	if w := do(t, h, req{method: "POST", path: "/api/jobs/" + strconv.FormatInt(j.ID, 10) + "/cancel"}); w.Code != 202 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	if got := waitJob(t, h, j.ID); got.Status != store.JobCancelled {
		t.Fatalf("status: %s", got.Status)
	}

	// Stop marks the job as stopping; kill ends it.
	decodeInto(do(t, h, req{method: "POST", path: "/api/projects/" + strconv.FormatInt(p.ID, 10) + "/jobs",
		body: `{"spec":{"goals":["slow"]}}`}), &j)
	time.Sleep(200 * time.Millisecond)
	id := strconv.FormatInt(j.ID, 10)
	w := do(t, h, req{method: "POST", path: "/api/jobs/" + id + "/stop"})
	var v jobView
	decodeInto(w, &v)
	if w.Code != 202 || (v.Status == store.JobRunning && v.StopRequestedAt == nil) {
		t.Fatalf("stop: %d %s", w.Code, w.Body)
	}
	do(t, h, req{method: "POST", path: "/api/jobs/" + id + "/kill"})
	if got := waitJob(t, h, j.ID); got.Status != store.JobCancelled || got.StopRequestedAt != nil {
		t.Fatalf("after kill: %+v", got)
	}
	if w := do(t, h, req{method: "POST", path: "/api/jobs/" + id + "/kill"}); w.Code != 409 {
		t.Fatalf("kill finished: %d", w.Code)
	}
}

func TestBuildEnv(t *testing.T) {
	env := buildEnv([]string{"PATH=/usr/bin", "JAVA_HOME=/old", "HOME=/h"}, map[string]string{"MAVEN_OPTS": "-Xmx1g"}, "/jdk")
	got := strings.Join(env, ";")
	if !strings.Contains(got, "JAVA_HOME=/jdk") || !strings.Contains(got, "PATH=/jdk/bin:/usr/bin") ||
		!strings.Contains(got, "MAVEN_OPTS=-Xmx1g") || strings.Contains(got, "/old") {
		t.Fatalf("env: %s", got)
	}
}

func decodeInto(w *httptest.ResponseRecorder, v any) {
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		panic("decode " + w.Body.String() + ": " + err.Error())
	}
}
