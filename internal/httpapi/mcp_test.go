package httpapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/binarycat0/buildyard/internal/store"
)

func TestMCP(t *testing.T) {
	home := t.TempDir()
	h, _ := newServerHome(t, home)
	n := 0
	rpc := func(method, params string) map[string]any {
		t.Helper()
		n++
		w := do(t, h, req{method: "POST", path: "/mcp", body: `{"jsonrpc":"2.0","id":` + strconv.Itoa(n) + `,"method":"` + method + `","params":` + params + `}`})
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", method, w.Code, w.Body)
		}
		var resp struct {
			Result map[string]any `json:"result"`
			Error  *rpcError      `json:"error"`
		}
		json.Unmarshal(w.Body.Bytes(), &resp)
		if resp.Error != nil {
			t.Fatalf("%s: %+v", method, resp.Error)
		}
		return resp.Result
	}
	// tool calls the named tool; its JSON text goes into v. It returns the text and isError.
	tool := func(name, args string, v any) (string, bool) {
		t.Helper()
		res := rpc("tools/call", `{"name":"`+name+`","arguments":`+args+`}`)
		text := res["content"].([]any)[0].(map[string]any)["text"].(string)
		if v != nil {
			json.Unmarshal([]byte(text), v)
		}
		isErr, _ := res["isError"].(bool)
		return text, isErr
	}

	init := rpc("initialize", `{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}`)
	if init["protocolVersion"] != "2025-06-18" || init["capabilities"].(map[string]any)["tools"] == nil {
		t.Fatalf("initialize: %v", init)
	}
	if w := do(t, h, req{method: "POST", path: "/mcp", body: `{"jsonrpc":"2.0","method":"notifications/initialized"}`}); w.Code != 202 {
		t.Fatalf("notification: %d", w.Code)
	}
	if w := do(t, h, req{method: "GET", path: "/mcp"}); w.Code != 405 {
		t.Fatalf("GET /mcp: %d", w.Code)
	}
	if tools := rpc("tools/list", `{}`)["tools"].([]any); len(tools) != len(mcpTools) {
		t.Fatalf("tools/list: %v", tools)
	}

	root := mavenProject(t)
	var p store.Project
	decodeInto(do(t, h, req{method: "POST", path: "/api/projects", body: `{"name":"app","rootPath":"` + root + `"}`}), &p)
	pid := strconv.FormatInt(p.ID, 10)
	do(t, h, req{method: "POST", path: "/api/projects/" + pid + "/profiles", body: `{"name":"Quick","spec":{"goals":["install"]}}`})

	var projects []struct {
		Name  string    `json:"name"`
		Tasks []mcpTask `json:"tasks"`
	}
	tool("list_projects", `{}`, &projects)
	if len(projects) != 1 || len(projects[0].Tasks) != 1 || !strings.HasSuffix(projects[0].Tasks[0].Command, "mvnw -B install") {
		t.Fatalf("list_projects: %+v", projects)
	}

	if text, isErr := tool("run_task", `{"project":"app","task":"nope"}`, nil); !isErr || !strings.Contains(text, "no task") {
		t.Fatalf("unknown task: %s", text)
	}
	var run struct {
		Job     int64  `json:"job"`
		Command string `json:"command"`
	}
	tool("run_task", `{"project":`+pid+`,"task":"quick","modules":[":core"],"properties":{"test":"FooTest"},"extra_args":["fail"]}`, &run)
	if !strings.HasSuffix(run.Command, "mvnw -B -pl :core -Dtest=FooTest install fail") {
		t.Fatalf("run_task: %+v", run)
	}

	var job struct {
		Job struct {
			Status    string `json:"status"`
			ProfileID *int64 `json:"profileId"`
		} `json:"job"`
		Log logPage `json:"log"`
	}
	tool("get_job", `{"job":`+strconv.FormatInt(run.Job, 10)+`,"wait_seconds":10,"log_lines":2}`, &job)
	if job.Job.Status != store.JobFailed || job.Job.ProfileID == nil || job.Log.Total != 3 ||
		strings.Join(job.Log.Lines, "|") != "foo: |[ERROR] boom" {
		t.Fatalf("get_job: %+v", job)
	}
	if text, isErr := tool("cancel_job", `{"job":`+strconv.FormatInt(run.Job, 10)+`}`, nil); !isErr || !strings.Contains(text, "already failed") {
		t.Fatalf("cancel finished: %s", text)
	}

	// Settings are readable, with secrets masked.
	os.MkdirAll(filepath.Join(home, ".m2"), 0o755)
	os.WriteFile(filepath.Join(home, ".m2", "settings.xml"), []byte("<settings><servers><server><id>x</id><password>hunter2</password></server></servers></settings>"), 0o644)
	var settings struct {
		ConfigFiles []struct {
			ID     string `json:"id"`
			Exists bool   `json:"exists"`
		} `json:"configFiles"`
		Project *struct {
			Dir string `json:"dir"`
		} `json:"project"`
	}
	tool("get_settings", `{"project":"app"}`, &settings)
	if settings.Project == nil || settings.Project.Dir != root || len(settings.ConfigFiles) == 0 {
		t.Fatalf("get_settings: %+v", settings)
	}
	text, _ := tool("read_config_file", `{"id":"maven.settings"}`, nil)
	if strings.Contains(text, "hunter2") || !strings.Contains(text, "<password>***</password>") {
		t.Fatalf("read_config_file: %s", text)
	}
}
