package httpapi

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/binarycat0/buildyard/internal/store"
	"github.com/binarycat0/buildyard/internal/tasks"
)

func TestModuleDiscoveryRetriesOnline(t *testing.T) {
	h, _ := newServer(t)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		return do(t, h, req{method: method, path: path, body: body})
	}
	fixture, _ := filepath.Abs("../../testdata/maven-logs/07-active-profiles.log")
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "pom.xml"), []byte("<project/>"), 0o644)
	// Offline fails like a missing help plugin; online prints the fixture. Args are logged.
	os.WriteFile(filepath.Join(root, "mvnw"), []byte(`#!/bin/sh
echo "$*" >> "`+root+`/calls"
case "$*" in *" -o "*) echo "[ERROR] Cannot access central in offline mode"; exit 1 ;; esac
cat "`+fixture+`"
`), 0o755)
	var p store.Project
	decodeInto(call("POST", "/api/projects", `{"rootPath":"`+root+`"}`), &p)
	pid := strconv.FormatInt(p.ID, 10)

	w := call("POST", "/api/projects/"+pid+"/modules/refresh", `{"spec":{"profiles":["with-extra"],"skips":["tests"],"goals":["install"]}}`)
	if w.Code != 202 {
		t.Fatalf("refresh: %d %s", w.Code, w.Body)
	}
	var task tasks.Task
	decodeInto(w, &task)
	deadline := time.Now().Add(10 * time.Second)
	for task.Status != tasks.Succeeded && task.Status != tasks.Failed && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		decodeInto(call("GET", "/api/tasks/"+strconv.FormatInt(task.ID, 10), ""), &task)
	}
	if task.Status != tasks.Succeeded {
		t.Fatalf("task: %+v", task)
	}
	calls, _ := os.ReadFile(filepath.Join(root, "calls"))
	if got := strings.TrimSpace(string(calls)); got != "-B -o -P with-extra -DskipTests help:active-profiles\n-B -P with-extra -DskipTests help:active-profiles" {
		t.Fatalf("calls:\n%s", got)
	}
	var mods modulesResponse
	decodeInto(call("GET", "/api/projects/"+pid+"/modules", ""), &mods)
	if len(mods.Modules) != 4 || mods.Modules[2].Key != "demo:demo-app" || mods.Modules[2].Name != "Demo App" ||
		mods.DiscoveredAt == nil || len(mods.SkipPresets) == 0 {
		t.Fatalf("modules: %+v", mods)
	}
}

func TestSavedTasks(t *testing.T) {
	h, _ := newServer(t)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		return do(t, h, req{method: method, path: path, body: body})
	}
	root := mavenProject(t)
	var p store.Project
	decodeInto(call("POST", "/api/projects", `{"rootPath":"`+root+`"}`), &p)
	pid := strconv.FormatInt(p.ID, 10)

	// Project presets: add one, drop a default; invalid ones are refused.
	if w := call("PATCH", "/api/projects/"+pid, `{"skipPresets":[{"id":"no-ui","label":"UI","args":["-Dno-ui"]},{"id":"sources"}]}`); w.Code != 200 {
		t.Fatalf("presets: %d %s", w.Code, w.Body)
	}
	if w := call("PATCH", "/api/projects/"+pid, `{"skipPresets":[{"id":"a b"}]}`); w.Code != 400 {
		t.Fatalf("bad preset: %d", w.Code)
	}

	spec := `{"goals":["install"],"modules":{"mode":"selected","list":[":core"],"alsoMake":true},"skips":["tests","no-ui"],"flags":{"threads":"1C"}}`
	w := call("POST", "/api/projects/"+pid+"/profiles", `{"name":" Fast core ","spec":`+spec+`}`)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var prof store.RunProfile
	decodeInto(w, &prof)
	if prof.Name != "Fast core" || !strings.Contains(string(prof.Spec), `"logLevel":"default"`) {
		t.Fatalf("profile: %+v %s", prof, prof.Spec)
	}
	if w := call("POST", "/api/projects/"+pid+"/profiles", `{"name":"Fast core","spec":`+spec+`}`); w.Code != 409 {
		t.Fatalf("duplicate: %d", w.Code)
	}
	if w := call("POST", "/api/projects/"+pid+"/profiles", `{"name":"bad","spec":{"goals":["install"],"skips":["sources"]}}`); w.Code != 400 {
		t.Fatalf("removed preset: %d", w.Code)
	}

	// Run by task id: the job links to the task and uses its spec.
	w = call("POST", "/api/projects/"+pid+"/jobs", `{"profileId":`+strconv.FormatInt(prof.ID, 10)+`}`)
	if w.Code != 201 {
		t.Fatalf("run: %d %s", w.Code, w.Body)
	}
	var j jobView
	decodeInto(w, &j)
	if j.ProfileID == nil || *j.ProfileID != prof.ID ||
		strings.Join(j.Argv[1:], " ") != "-B -T 1C -pl :core -am -DskipTests -Dno-ui install" {
		t.Fatalf("job: %+v", j)
	}
	waitJob(t, h, j.ID)

	// Update, pin, list with last job, delete.
	w = call("PUT", "/api/profiles/"+strconv.FormatInt(prof.ID, 10), `{"name":"Core","pinned":true,"spec":{"goals":["verify"]}}`)
	decodeInto(w, &prof)
	if w.Code != 200 || prof.Name != "Core" || !prof.Pinned || prof.LastJob == nil || prof.LastJob.ID != j.ID {
		t.Fatalf("update: %d %s", w.Code, w.Body)
	}
	call("POST", "/api/projects/"+pid+"/profiles", `{"name":"Another","spec":{"goals":["test"]}}`)
	var list []store.RunProfile
	decodeInto(call("GET", "/api/projects/"+pid+"/profiles", ""), &list)
	if len(list) != 2 || list[0].Name != "Core" {
		t.Fatalf("list: %+v", list)
	}
	if w := call("DELETE", "/api/profiles/"+strconv.FormatInt(prof.ID, 10), ""); w.Code != 204 {
		t.Fatalf("delete: %d", w.Code)
	}
	var job jobView
	decodeInto(call("GET", "/api/jobs/"+strconv.FormatInt(j.ID, 10), ""), &job)
	if job.ProfileID != nil || !strings.Contains(string(job.Spec), `":core"`) {
		t.Fatalf("job after task delete: %+v", job)
	}
}

// With the extension, discovery stores folders, aggregators and dependencies.
func TestModuleDiscoveryTree(t *testing.T) {
	srv, _ := newTestServer(t, t.TempDir())
	srv.ExtJar = "/opt/buildyard/ext.jar"
	h := srv.Handler()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		return do(t, h, req{method: method, path: path, body: body})
	}
	fixture, _ := filepath.Abs("../../testdata/maven-logs/07-active-profiles.log")
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "pom.xml"), []byte("<project/>"), 0o644)
	// Like the extension: write the session event to the file named by -Dbuildyard.events.
	os.WriteFile(filepath.Join(root, "mvnw"), []byte(`#!/bin/sh
for a in "$@"; do
  case "$a" in
    -Dmaven.ext.class.path=*) echo "$a" > "`+root+`/ext" ;;
    -Dbuildyard.events=*) ev="${a#-Dbuildyard.events=}" ;;
  esac
done
R="`+root+`"
printf '{"t":"session","projects":[{"key":"demo:demo-parent","dir":"%s","deps":[]},{"key":"demo:demo-core","dir":"%s/core","aggregator":"demo:demo-parent","deps":["demo:demo-parent"]},{"key":"demo:demo-app","dir":"%s/app","aggregator":"demo:demo-parent","deps":["demo:demo-parent","demo:demo-core"]},{"key":"demo:demo-extra","dir":"%s/extra","aggregator":"demo:demo-parent","deps":[]}]}\n' "$R" "$R" "$R" "$R" > "$ev"
cat "`+fixture+`"
`), 0o755)
	var p store.Project
	decodeInto(call("POST", "/api/projects", `{"rootPath":"`+root+`"}`), &p)
	pid := strconv.FormatInt(p.ID, 10)
	var task tasks.Task
	decodeInto(call("POST", "/api/projects/"+pid+"/modules/refresh", ""), &task)
	deadline := time.Now().Add(10 * time.Second)
	for task.Status != tasks.Succeeded && task.Status != tasks.Failed && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		decodeInto(call("GET", "/api/tasks/"+strconv.FormatInt(task.ID, 10), ""), &task)
	}
	if task.Status != tasks.Succeeded || !strings.Contains(fmt.Sprint(task.Result), "tree:true") {
		t.Fatalf("task: %+v", task)
	}
	if ext, _ := os.ReadFile(filepath.Join(root, "ext")); strings.TrimSpace(string(ext)) != "-Dmaven.ext.class.path=/opt/buildyard/ext.jar" {
		t.Fatalf("ext arg: %q", ext)
	}
	var mods modulesResponse
	decodeInto(call("GET", "/api/projects/"+pid+"/modules", ""), &mods)
	app := mods.Modules[2]
	if mods.Modules[0].RelPath != "" || app.RelPath != "app" || app.Aggregator != "demo:demo-parent" ||
		strings.Join(app.Deps, ",") != "demo:demo-parent,demo:demo-core" {
		t.Fatalf("modules: %+v", mods.Modules)
	}
}
