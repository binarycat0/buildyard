package runner

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/binarycat0/buildyard/internal/store"
)

func setup(t *testing.T, maxConcurrent int) (*Runner, *store.Store, int64) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	p, err := st.CreateProject(context.Background(), store.Project{Name: "p", RootPath: dir, BuildTool: "maven"})
	if err != nil {
		t.Fatal(err)
	}
	logs := filepath.Join(dir, "logs")
	os.MkdirAll(logs, 0o755)
	r := New(st, logs, maxConcurrent, 0, slog.New(slog.DiscardHandler))
	r.KillGrace = 500 * time.Millisecond
	t.Cleanup(func() { r.Shutdown(context.Background()) })
	return r, st, p.ID
}

func sh(dir, script string) Command {
	return Command{Argv: []string{"/bin/sh", "-c", script}, Dir: dir, Env: os.Environ()}
}

func wait(t *testing.T, st *store.Store, id int64) store.Job {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		j, err := st.GetJob(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if j.Finished() {
			return j
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %d did not finish", id)
	return store.Job{}
}

func waitLines(t *testing.T, r *Runner, id int64, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if r.Lines(id) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %d: fewer than %d lines", id, n)
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func TestSuccessFailureAndLog(t *testing.T) {
	r, st, pid := setup(t, 1)
	ctx := context.Background()
	ok, err := r.Enqueue(ctx, store.Job{ProjectID: pid}, sh(t.TempDir(), `echo one; echo two >&2; printf 'no newline'`))
	if err != nil {
		t.Fatal(err)
	}
	j := wait(t, st, ok.ID)
	if j.Status != store.JobSucceeded || *j.ExitCode != 0 || j.LogLines != 3 || j.StartedAt == nil {
		t.Fatalf("ok job: %+v", j)
	}
	lines, n, _ := ReadLines(j.LogPath, 2, 10)
	if n != 3 || strings.Join(lines, "|") != "two|no newline" {
		t.Fatalf("log: %d %q", n, lines)
	}

	bad, _ := r.Enqueue(ctx, store.Job{ProjectID: pid}, sh(t.TempDir(), `exit 3`))
	if j := wait(t, st, bad.ID); j.Status != store.JobFailed || *j.ExitCode != 3 {
		t.Fatalf("failed job: %+v", j)
	}
	missing, _ := r.Enqueue(ctx, store.Job{ProjectID: pid}, Command{Argv: []string{"/no/such/mvn"}, Dir: t.TempDir()})
	if j := wait(t, st, missing.ID); j.Status != store.JobFailed || j.ExitCode != nil || j.LogLines != 1 {
		t.Fatalf("missing executable: %+v", j)
	}
}

// The plan's done criterion: cancelling a script that spawned children leaves no processes.
func TestCancelKillsProcessGroup(t *testing.T) {
	r, st, pid := setup(t, 1)
	dir := t.TempDir()
	// One child that obeys SIGTERM, one that ignores it, and a parent that ignores it too.
	script := `sleep 300 & echo $!; (trap '' TERM; sleep 300) & echo $!; trap '' TERM; echo ready; wait`
	j, err := r.Enqueue(context.Background(), store.Job{ProjectID: pid}, sh(dir, script))
	if err != nil {
		t.Fatal(err)
	}
	waitLines(t, r, j.ID, 3)
	lines, _, _ := ReadLines(j.LogPath, 1, 2)
	var pids []int
	for _, l := range lines {
		p, err := strconv.Atoi(l)
		if err != nil {
			t.Fatalf("pid line %q", l)
		}
		pids = append(pids, p)
	}
	if err := r.Cancel(j.ID); err != nil {
		t.Fatal(err)
	}
	if got := wait(t, st, j.ID); got.Status != store.JobCancelled {
		t.Fatalf("status %s", got.Status)
	}
	deadline := time.Now().Add(5 * time.Second)
	for _, p := range pids {
		for alive(p) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if alive(p) {
			// Zombies count as gone: they are only waiting to be reaped by their parent (init).
			if b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/stat", p)); !strings.Contains(string(b), ") Z") {
				t.Fatalf("process %d survived cancel", p)
			}
		}
	}
	if err := r.Cancel(j.ID); err != ErrNotActive {
		t.Fatalf("second cancel: %v", err)
	}
}

func TestJobsOfOneProjectRunSequentially(t *testing.T) {
	r, st, pid := setup(t, 4)
	ctx := context.Background()
	other, _ := st.CreateProject(ctx, store.Project{Name: "q", RootPath: t.TempDir(), BuildTool: "maven"})
	a, _ := r.Enqueue(ctx, store.Job{ProjectID: pid}, sh(t.TempDir(), `sleep 0.3`))
	b, _ := r.Enqueue(ctx, store.Job{ProjectID: pid}, sh(t.TempDir(), `sleep 0.1`))
	c, _ := r.Enqueue(ctx, store.Job{ProjectID: other.ID}, sh(t.TempDir(), `sleep 0.1`))
	ja, jb, jc := wait(t, st, a.ID), wait(t, st, b.ID), wait(t, st, c.ID)
	if jb.StartedAt.Before(*ja.FinishedAt) {
		t.Fatalf("same project overlapped: a finished %v, b started %v", ja.FinishedAt, jb.StartedAt)
	}
	if !jc.StartedAt.Before(*ja.FinishedAt) {
		t.Fatalf("another project should run in parallel: a %v-%v, c %v", ja.StartedAt, ja.FinishedAt, jc.StartedAt)
	}

	// Cancelling a queued job never starts it.
	d, _ := r.Enqueue(ctx, store.Job{ProjectID: pid}, sh(t.TempDir(), `sleep 1`))
	e, _ := r.Enqueue(ctx, store.Job{ProjectID: pid}, sh(t.TempDir(), `echo never`))
	if err := r.Cancel(e.ID); err != nil {
		t.Fatal(err)
	}
	if je := wait(t, st, e.ID); je.Status != store.JobCancelled || je.StartedAt != nil {
		t.Fatalf("queued cancel: %+v", je)
	}
	r.Cancel(d.ID)
	wait(t, st, d.ID)
}

// A subscriber that joins mid-run gets every line exactly once: the file up to From, then the channel.
func TestLateSubscriberGetsWholeLog(t *testing.T) {
	r, st, pid := setup(t, 1)
	j, _ := r.Enqueue(context.Background(), store.Job{ProjectID: pid},
		sh(t.TempDir(), `i=1; while [ $i -le 400 ]; do echo line$i; i=$((i+1)); [ $i -eq 200 ] && sleep 0.3; done; true`))
	waitLines(t, r, j.ID, 50)
	sub := r.Subscribe(j.ID)
	if sub == nil {
		t.Fatal("no subscription")
	}
	got, _, _ := ReadLines(j.LogPath, 1, sub.From)
	if len(got) != sub.From {
		t.Fatalf("file has %d lines, From %d", len(got), sub.From)
	}
	var final string
	for ev := range sub.C {
		switch ev.Type {
		case "line":
			if ev.N != len(got)+1 {
				t.Fatalf("line %d after %d", ev.N, len(got))
			}
			got = append(got, ev.Text)
		case "status":
			final = ev.Status
		}
	}
	if len(got) != 400 || got[399] != "line400" || final != store.JobSucceeded {
		t.Fatalf("got %d lines, final %q", len(got), final)
	}
	wait(t, st, j.ID)
	if r.Subscribe(j.ID) != nil {
		t.Fatal("finished job should have no live bus")
	}
}

func TestInterruptUnfinished(t *testing.T) {
	_, st, pid := setup(t, 1)
	ctx := context.Background()
	j, _ := st.CreateJob(ctx, store.Job{ProjectID: pid, Argv: []string{"x"}}, func(int64) string { return "/x" })
	if n, err := st.InterruptUnfinished(ctx); err != nil || n != 1 {
		t.Fatalf("interrupt: %d %v", n, err)
	}
	if got, _ := st.GetJob(ctx, j.ID); got.Status != store.JobInterrupted || got.FinishedAt == nil {
		t.Fatalf("%+v", got)
	}
}

// Stop sends SIGINT and waits for the build to end by itself; Kill ends it at once.
func TestStopAndKill(t *testing.T) {
	r, st, pid := setup(t, 1)
	r.KillGrace = time.Hour // Stop and Kill must not rely on the Cancel timer
	dir := t.TempDir()
	// Handles SIGINT like Maven: prints, cleans up for a moment, exits 130.
	graceful := `trap 'echo stopping; sleep 0.3; echo cleaned; exit 130' INT; echo ready; while :; do sleep 0.05; done`
	j, _ := r.Enqueue(context.Background(), store.Job{ProjectID: pid}, sh(dir, graceful))
	waitLines(t, r, j.ID, 1)
	if err := r.Stop(j.ID); err != nil {
		t.Fatal(err)
	}
	if r.StopRequested(j.ID).IsZero() {
		t.Fatal("stop time not recorded")
	}
	got := wait(t, st, j.ID)
	lines, _, _ := ReadLines(got.LogPath, 1, 10)
	if got.Status != store.JobCancelled || strings.Join(lines, ",") != "ready,stopping,cleaned" {
		t.Fatalf("stop: %s %q", got.Status, lines)
	}

	// Ignores SIGINT and SIGTERM: only Kill ends it.
	stubborn := `trap '' INT TERM; echo ready; while :; do sleep 0.05; done`
	j, _ = r.Enqueue(context.Background(), store.Job{ProjectID: pid}, sh(dir, stubborn))
	waitLines(t, r, j.ID, 1)
	r.Stop(j.ID)
	time.Sleep(300 * time.Millisecond)
	if cur, _ := st.GetJob(context.Background(), j.ID); cur.Finished() {
		t.Fatal("stop should not kill")
	}
	if err := r.Kill(j.ID); err != nil {
		t.Fatal(err)
	}
	if got := wait(t, st, j.ID); got.Status != store.JobCancelled {
		t.Fatalf("kill: %s", got.Status)
	}
}

// evCmd runs script with $1 = the events file path the runner chose.
func evCmd(dir, script string) Command {
	return Command{Argv: []string{"/bin/sh"}, Dir: dir, Env: os.Environ(),
		EventArgs: func(path string) []string { return []string{"-c", script, "sh", path} }}
}

func TestModuleProgress(t *testing.T) {
	r, st, pid := setup(t, 1)
	ctx := context.Background()
	script := `
f="$1"
printf '%s\n' '{"t":"session","projects":[{"key":"g:a","name":"A"},{"key":"g:b","name":"B"},{"key":"g:c","name":"C"}]}' >> "$f"
printf '%s\n' '{"t":"project_start","key":"g:a","ts":1700000000000}' >> "$f"
printf '%s' '{"t":"project_end","key":"g:a","status":"succeeded",' >> "$f"
sleep 0.4
printf '%s\n' '"ms":1200,"ts":1700000001200}' >> "$f"
printf '%s\n' '{"t":"project_start","key":"g:b","ts":1700000001300}' >> "$f"
echo started-b
sleep 30`
	job, err := r.Enqueue(ctx, store.Job{ProjectID: pid}, evCmd(t.TempDir(), script))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(job.Argv, " ") != "/bin/sh" {
		t.Fatalf("recorded argv must leave the event args out: %q", job.Argv)
	}
	waitLines(t, r, job.ID, 1)
	sub := r.Subscribe(job.ID)
	defer sub.Close()

	var mods []store.JobModule
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mods = r.Modules(job.ID)
		if len(mods) == 3 && mods[1].Status == "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(mods) != 3 || mods[0].Status != "succeeded" || mods[0].DurationMs != 1200 || mods[1].Status != "running" || mods[2].Status != "pending" {
		t.Fatalf("live modules: %+v", mods)
	}
	if err := r.Kill(job.ID); err != nil {
		t.Fatal(err)
	}
	var sawB bool
	for ev := range sub.C {
		if ev.Type == "module" && ev.Module.Key == "g:b" && ev.Module.Status == "cancelled" {
			sawB = true
		}
	}
	if !sawB {
		t.Fatal("no final module event for the module that was running")
	}
	wait(t, st, job.ID)
	saved, err := st.ListJobModules(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, m := range saved {
		got = append(got, m.Name+"="+m.Status)
	}
	if strings.Join(got, ",") != "A=succeeded,B=cancelled,C=pending" {
		t.Fatalf("saved modules: %v", got)
	}
	if _, err := os.Stat(filepath.Join(r.logsDir, strconv.FormatInt(job.ID, 10)+".events.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("events file should be removed: %v", err)
	}
}

// fakeParser: "ERR x" is a problem; "MODS a,b" sets the module list; "RUN a" / "OK a" move one.
type fakeParser struct{ mods []store.JobModule }

func (f *fakeParser) Line(n int, text string) ([]store.JobProblem, bool) {
	cmd, arg, _ := strings.Cut(text, " ")
	switch cmd {
	case "ERR":
		return []store.JobProblem{{Kind: "compile", Message: arg, LogLine: n}}, false
	case "MODS":
		f.mods = nil
		for _, k := range strings.Split(arg, ",") {
			f.mods = append(f.mods, store.JobModule{Key: k, Name: k, Status: "pending"})
		}
		return nil, true
	case "RUN", "OK":
		for i := range f.mods {
			if f.mods[i].Key == arg {
				f.mods[i].Status = map[string]string{"RUN": "running", "OK": "succeeded"}[cmd]
			}
		}
		return nil, true
	}
	return nil, false
}

func (f *fakeParser) Modules() []store.JobModule { return append([]store.JobModule(nil), f.mods...) }

func TestProblemsAndLogFallback(t *testing.T) {
	r, st, pid := setup(t, 1)
	ctx := context.Background()
	cmd := sh(t.TempDir(), `echo MODS a,b; echo RUN a; echo ERR boom; sleep 0.2; echo OK a; echo RUN b; echo OK b`)
	cmd.Parser = func() OutputParser { return &fakeParser{} }
	job, err := r.Enqueue(ctx, store.Job{ProjectID: pid}, cmd)
	if err != nil {
		t.Fatal(err)
	}
	wait(t, st, job.ID)
	probs, _ := st.ListJobProblems(ctx, job.ID)
	if len(probs) != 1 || probs[0].Message != "boom" || probs[0].LogLine != 3 {
		t.Fatalf("problems: %+v", probs)
	}
	mods, _ := st.ListJobModules(ctx, job.ID)
	if len(mods) != 2 || mods[0].Status != "succeeded" || mods[1].Status != "succeeded" || mods[0].DurationMs < 150 {
		t.Fatalf("modules from the log: %+v", mods)
	}

	// Extension events replace the log's module list.
	script := `printf '%s\n' '{"t":"session","projects":[{"key":"g:x","name":"X"}]}' >> "$1"; echo MODS a,b; sleep 0.6; printf '%s\n' '{"t":"project_start","key":"g:x","ts":1}' '{"t":"project_end","key":"g:x","status":"succeeded","ms":5,"ts":6}' >> "$1"; echo RUN a; sleep 0.4`
	cmd = evCmd(t.TempDir(), script)
	cmd.Parser = func() OutputParser { return &fakeParser{} }
	job, err = r.Enqueue(ctx, store.Job{ProjectID: pid}, cmd)
	if err != nil {
		t.Fatal(err)
	}
	wait(t, st, job.ID)
	mods, _ = st.ListJobModules(ctx, job.ID)
	if len(mods) != 1 || mods[0].Key != "g:x" || mods[0].Status != "succeeded" {
		t.Fatalf("events must win: %+v", mods)
	}
}

func TestSetLimitsStartsQueuedJobs(t *testing.T) {
	r, st, pid := setup(t, 1)
	ctx := context.Background()
	other, err := st.CreateProject(ctx, store.Project{Name: "q", RootPath: t.TempDir(), BuildTool: "maven"})
	if err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(t.TempDir(), "gate")
	first, err := r.Enqueue(ctx, store.Job{ProjectID: pid}, sh(t.TempDir(), `echo started; while [ ! -e `+gate+` ]; do sleep 0.05; done`))
	if err != nil {
		t.Fatal(err)
	}
	waitLines(t, r, first.ID, 1)
	second, err := r.Enqueue(ctx, store.Job{ProjectID: other.ID}, sh(t.TempDir(), `echo second`))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if j, _ := st.GetJob(ctx, second.ID); j.Status != store.JobQueued {
		t.Fatalf("with one slot the second job is %s, want queued", j.Status)
	}
	r.SetLimits(2, 0) // a second slot: the queued job starts while the first still runs
	if j := wait(t, st, second.ID); j.Status != store.JobSucceeded {
		t.Fatalf("second job %s", j.Status)
	}
	os.WriteFile(gate, nil, 0o600)
	wait(t, st, first.ID)
}
