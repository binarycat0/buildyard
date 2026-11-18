package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/binarycat0/buildyard/internal/store"
	"github.com/binarycat0/buildyard/internal/tasks"
)

func fakeJDK(t *testing.T, home, version string) {
	t.Helper()
	os.MkdirAll(filepath.Join(home, "bin"), 0o755)
	os.WriteFile(filepath.Join(home, "bin/java"), []byte("#!/bin/sh\n"), 0o755)
	os.WriteFile(filepath.Join(home, "release"), []byte(`JAVA_VERSION="`+version+`"`+"\nIMPLEMENTOR=\"Eclipse Adoptium\"\n"), 0o644)
}

func TestJDKDetectSettingsAndRuntime(t *testing.T) {
	userHome := t.TempDir()
	fakeJDK(t, filepath.Join(userHome, ".sdkman/candidates/java/21.0.8-tem"), "21.0.8")
	fakeJDK(t, filepath.Join(userHome, ".sdkman/candidates/java/17.0.12-tem"), "17.0.12")
	h, _ := newServerHome(t, userHome)

	call := func(method, path, body string) *httptest.ResponseRecorder {
		return do(t, h, req{method: method, path: path, body: body})
	}
	decodeInto := func(w *httptest.ResponseRecorder, v any) {
		t.Helper()
		if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
			t.Fatalf("decode %d %s: %v", w.Code, w.Body, err)
		}
	}

	// Detection is a background task: 202 with a task id, then poll.
	w := call("POST", "/api/jdks/detect", "")
	if w.Code != 202 {
		t.Fatalf("detect: %d %s", w.Code, w.Body)
	}
	var task tasks.Task
	decodeInto(w, &task)
	deadline := time.Now().Add(3 * time.Second)
	for task.Status != tasks.Succeeded {
		if task.Status == tasks.Failed || time.Now().After(deadline) {
			t.Fatalf("task: %+v", task)
		}
		time.Sleep(10 * time.Millisecond)
		decodeInto(call("GET", "/api/tasks/"+strconv.FormatInt(task.ID, 10), ""), &task)
	}
	var jdks []store.JDK
	decodeInto(call("GET", "/api/jdks", ""), &jdks)
	if len(jdks) != 2 || jdks[0].Major != 21 || jdks[1].Major != 17 {
		t.Fatalf("jdks: %+v", jdks)
	}
	var managers []jdkManager
	decodeInto(call("GET", "/api/jdk-managers", ""), &managers)
	sdk := -1
	for i, m := range managers {
		if m.ID == "sdkman" {
			sdk = i
		}
	}
	if sdk < 0 || !managers[sdk].Installed || managers[sdk].JDKs != 2 {
		t.Fatalf("managers: %+v", managers)
	}

	// Build tool detection is a background task too; its result shows up in /api/tools.
	w = call("POST", "/api/tools/detect", "")
	if w.Code != 202 {
		t.Fatalf("tools detect: %d", w.Code)
	}
	decodeInto(w, &task)
	for task.Status != tasks.Succeeded {
		if task.Status == tasks.Failed || time.Now().After(deadline) {
			t.Fatalf("tools task: %+v", task)
		}
		time.Sleep(10 * time.Millisecond)
		decodeInto(call("GET", "/api/tasks/"+strconv.FormatInt(task.ID, 10), ""), &task)
	}
	var tools struct {
		Installed []struct{ ID string } `json:"installed"`
	}
	decodeInto(call("GET", "/api/tools", ""), &tools)
	if len(tools.Installed) != 2 || tools.Installed[1].ID != "mvnd" {
		t.Fatalf("tools: %+v", tools)
	}

	if w := call("GET", "/api/tasks/999", ""); w.Code != 404 {
		t.Fatalf("missing task: %d", w.Code)
	}

	// Manual JDK: bad path, good path, duplicate.
	if w := call("POST", "/api/jdks", `{"home":"`+t.TempDir()+`"}`); w.Code != 400 {
		t.Fatalf("not a jdk: %d", w.Code)
	}
	graal := filepath.Join(t.TempDir(), "graal")
	fakeJDK(t, graal, "21.0.2")
	if w := call("POST", "/api/jdks", `{"home":"`+graal+`","name":"Graal"}`); w.Code != 201 {
		t.Fatalf("add: %d %s", w.Code, w.Body)
	}
	if w := call("POST", "/api/jdks", `{"home":"`+graal+`"}`); w.Code != 409 {
		t.Fatalf("dup: %d", w.Code)
	}

	// Global settings.
	j17 := strconv.FormatInt(jdks[1].ID, 10)
	w = call("PUT", "/api/settings", `{"defaultExecutable":"mvnd","defaultJdkId":`+j17+`}`)
	var st store.Settings
	decodeInto(w, &st)
	if w.Code != 200 || st.DefaultExecutable != "mvnd" || *st.DefaultJdkID != jdks[1].ID {
		t.Fatalf("settings: %d %s", w.Code, w.Body)
	}
	if w := call("PUT", "/api/settings", `{"defaultExecutable":"gradle"}`); w.Code != 400 {
		t.Fatalf("bad executable: %d", w.Code)
	}

	// Project with a wrapper: default is mvnd (not installed here) → error shown, project override → wrapper.
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "pom.xml"), []byte("<project/>"), 0o644)
	os.WriteFile(filepath.Join(root, "mvnw"), []byte("#!/bin/sh"), 0o755)
	var p store.Project
	decodeInto(call("POST", "/api/projects", `{"rootPath":"`+root+`"}`), &p)
	rt := "/api/projects/" + strconv.FormatInt(p.ID, 10) + "/runtime"

	var info runtimeInfo
	decodeInto(call("GET", rt, ""), &info)
	if info.Executable != nil || info.ExecutableError == "" || info.ExecutableFrom != "default" {
		t.Fatalf("default mvnd should be unavailable: %+v", info)
	}
	if info.JDK == nil || info.JDK.Major != 17 || info.JDKFrom != "default" || info.Wrapper == nil {
		t.Fatalf("default jdk: %+v", info)
	}

	j21 := strconv.FormatInt(jdks[0].ID, 10)
	call("PATCH", "/api/projects/"+strconv.FormatInt(p.ID, 10), `{"executable":"wrapper","jdkId":`+j21+`}`)
	info = runtimeInfo{}
	decodeInto(call("GET", rt, ""), &info)
	if info.Executable == nil || info.Executable.Kind != "wrapper" || info.ExecutableFrom != "project" {
		t.Fatalf("wrapper: %+v", info)
	}
	if info.JDK == nil || info.JDK.Major != 21 || info.JDKFrom != "project" {
		t.Fatalf("project jdk: %+v", info)
	}

	// Reset project to defaults with "" and 0.
	call("PATCH", "/api/projects/"+strconv.FormatInt(p.ID, 10), `{"executable":"","jdkId":0}`)
	info = runtimeInfo{}
	decodeInto(call("GET", rt, ""), &info)
	if info.ExecutableFrom != "default" || info.JDKFrom != "default" {
		t.Fatalf("reset: %+v", info)
	}
}
