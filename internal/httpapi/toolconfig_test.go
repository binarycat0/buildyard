package httpapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolConfigEditing(t *testing.T) {
	userHome := t.TempDir()
	h, _ := newServerHome(t, userHome)
	call := func(method, path, body string) (int, map[string]any) {
		t.Helper()
		w := do(t, h, req{method: method, path: path, body: body})
		var v map[string]any
		json.Unmarshal(w.Body.Bytes(), &v)
		return w.Code, v
	}

	w := do(t, h, req{method: "GET", path: "/api/tool-configs"})
	var list []map[string]any
	json.Unmarshal(w.Body.Bytes(), &list)
	if w.Code != 200 || len(list) < 5 {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}

	code, f := call("GET", "/api/tool-configs/mvnd.properties", "")
	if code != 200 || f["exists"] != false || f["sha"] != "" {
		t.Fatalf("get missing: %d %v", code, f)
	}
	code, res := call("PUT", "/api/tool-configs/mvnd.properties", `{"content":"mvnd.maxHeapSize=4g\n","baseSha":""}`)
	if code != 200 {
		t.Fatalf("create: %d %v", code, res)
	}
	sha := res["file"].(map[string]any)["sha"].(string)
	data, _ := os.ReadFile(filepath.Join(userHome, ".m2/mvnd.properties"))
	if string(data) != "mvnd.maxHeapSize=4g\n" {
		t.Fatalf("on disk: %q", data)
	}
	if code, _ := call("PUT", "/api/tool-configs/mvnd.properties", `{"content":"x\n","baseSha":""}`); code != 409 {
		t.Fatalf("stale save: %d", code)
	}
	code, res = call("PUT", "/api/tool-configs/mvnd.properties", `{"content":"mvnd.maxHeapSize=8g\n","baseSha":"`+sha+`"}`)
	backup, _ := res["backup"].(string)
	if code != 200 || backup == "" {
		t.Fatalf("update: %d %v", code, res)
	}
	if code, res := call("GET", "/api/tool-configs/mvnd.properties/backups/"+backup, ""); code != 200 || res["content"] != "mvnd.maxHeapSize=4g\n" {
		t.Fatalf("backup: %d %v", code, res)
	}
	if code, _ := call("PUT", "/api/tool-configs/maven.settings", `{"content":"<settings>","baseSha":""}`); code != 400 {
		t.Fatalf("broken XML: %d", code)
	}
	if code, _ := call("GET", "/api/tool-configs/nope", ""); code != 404 {
		t.Fatalf("unknown: %d", code)
	}

	fakeJDK(t, filepath.Join(userHome, "jdk21"), "21.0.8")
	call("POST", "/api/jdks", `{"home":"`+filepath.Join(userHome, "jdk21")+`"}`)
	code, res = call("GET", "/api/toolchains/generate", "")
	if code != 200 || res["jdks"].(float64) != 1 || !strings.Contains(res["content"].(string), "<version>21.0.8</version>") {
		t.Fatalf("toolchains: %d %v", code, res)
	}
}
