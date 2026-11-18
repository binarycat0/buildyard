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
