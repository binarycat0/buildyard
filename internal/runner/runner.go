// Package runner runs jobs: a global queue with at most one job per project at a time,
// each job in its own process group, output in a log file and on a live bus for SSE.
package runner

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/binarycat0/buildyard/internal/store"
)

// Command is what a job executes.
type Command struct {
	Argv []string
	Dir  string
	Env  []string // full environment
	// SignalLeaderOnly: cancel sends SIGINT to the main process only and never kills the
	// group. Used for mvnd, whose client must not take the long-lived daemon down with it.
	SignalLeaderOnly bool
}

// Runner owns the job queue. Create it with New.
type Runner struct {
	store         *store.Store
	log           *slog.Logger
	logsDir       string
	maxConcurrent int
	keepJobs      int
	// KillGrace is how long a cancelled job gets between SIGTERM and SIGKILL.
	KillGrace time.Duration

	mu      sync.Mutex
	queue   []*run
	runs    map[int64]*run // queued and running jobs by id
	busy    map[int64]bool // projects with a running job
	active  int
	closing bool
	wg      sync.WaitGroup
}

// New returns a runner. maxConcurrent limits running jobs across all projects; keepJobs
// is how many finished jobs each project keeps (0 = keep all).
func New(st *store.Store, logsDir string, maxConcurrent, keepJobs int, log *slog.Logger) *Runner {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	if log == nil {
		log = slog.Default()
	}
	return &Runner{store: st, log: log, logsDir: logsDir, maxConcurrent: maxConcurrent, keepJobs: keepJobs,
		KillGrace: 10 * time.Second, runs: map[int64]*run{}, busy: map[int64]bool{}}
}

// ErrClosed is returned by Enqueue after Shutdown started.
var ErrClosed = errors.New("runner is shutting down")

// ErrNotActive is returned by Cancel for a job that is not queued or running.
var ErrNotActive = errors.New("job is not queued or running")

// Enqueue records a queued job and starts it as soon as its project and a slot are free.
func (r *Runner) Enqueue(ctx context.Context, j store.Job, cmd Command) (store.Job, error) {
	if len(cmd.Argv) == 0 {
		return store.Job{}, errors.New("empty command")
	}
	r.mu.Lock()
	closing := r.closing
	r.mu.Unlock()
	if closing {
		return store.Job{}, ErrClosed
	}
	j.Argv = cmd.Argv
	job, err := r.store.CreateJob(ctx, j, func(id int64) string {
		return filepath.Join(r.logsDir, strconv.FormatInt(id, 10)+".log")
	})
	if err != nil {
		return store.Job{}, err
	}
	rn := &run{job: job, cmd: cmd, subs: map[*Sub]struct{}{}, done: make(chan struct{})}
	r.mu.Lock()
	r.queue = append(r.queue, rn)
	r.runs[job.ID] = rn
	r.dispatchLocked()
	r.mu.Unlock()
	return job, nil
}

// dispatchLocked starts queued jobs whose project is idle while slots are free.
func (r *Runner) dispatchLocked() {
	if r.closing {
		return
	}
	rest := r.queue[:0]
	for _, rn := range r.queue {
		if r.active < r.maxConcurrent && !r.busy[rn.job.ProjectID] && !rn.cancelled {
			r.active++
			r.busy[rn.job.ProjectID] = true
			r.wg.Add(1)
			go r.execute(rn)
			continue
		}
		rest = append(rest, rn)
	}
	r.queue = rest
}

// Cancel stops a queued or running job: SIGTERM, then SIGKILL after KillGrace.
func (r *Runner) Cancel(id int64) error {
	return r.control(id, func(rn *run) { rn.stop(r.KillGrace, false) })
}

// Stop asks a running job to finish like Ctrl+C in a terminal (SIGINT): Maven runs its
// shutdown hooks and the build ends on its own. Nothing is killed; use Kill for that.
// A queued job is dropped.
func (r *Runner) Stop(id int64) error {
	return r.control(id, func(rn *run) { rn.signal(syscall.SIGINT, false) })
}

// Kill ends a running job at once (SIGKILL to its process group). A queued job is dropped.
func (r *Runner) Kill(id int64) error {
	return r.control(id, func(rn *run) { rn.signal(syscall.SIGKILL, false) })
}

func (r *Runner) control(id int64, running func(*run)) error {
	r.mu.Lock()
	rn, ok := r.runs[id]
	if !ok {
		r.mu.Unlock()
		return ErrNotActive
	}
	for i, q := range r.queue {
		if q == rn { // still queued: drop it without starting
			r.queue = append(r.queue[:i], r.queue[i+1:]...)
			delete(r.runs, id)
			r.mu.Unlock()
			r.finishQueued(rn, store.JobCancelled)
			return nil
		}
	}
	r.mu.Unlock()
	running(rn)
	return nil
}

// StopRequested returns when a stop or kill was first requested for an active job.
func (r *Runner) StopRequested(id int64) time.Time {
	r.mu.Lock()
	rn, ok := r.runs[id]
	r.mu.Unlock()
	if !ok {
		return time.Time{}
	}
	rn.mu.Lock()
	defer rn.mu.Unlock()
	return rn.stopAt
}

// Lines returns the live line count of an active job, or -1.
func (r *Runner) Lines(id int64) int {
	r.mu.Lock()
	rn, ok := r.runs[id]
	r.mu.Unlock()
	if !ok {
		return -1
	}
	rn.mu.Lock()
	defer rn.mu.Unlock()
	return rn.lines
}

// Subscribe attaches to an active job's live output. It returns nil when the job is not
// active. Lines up to and including Sub.From are already in the log file; the channel
// delivers everything after that, then is closed when the job ends (or the subscriber
// falls too far behind; Sub.Dropped tells which).
func (r *Runner) Subscribe(id int64) *Sub {
	r.mu.Lock()
	rn, ok := r.runs[id]
	r.mu.Unlock()
	if !ok {
		return nil
	}
	return rn.subscribe()
}

// Shutdown stops all jobs and marks them interrupted. Queued jobs never start.
func (r *Runner) Shutdown(ctx context.Context) {
	r.mu.Lock()
	r.closing = true
	queued := r.queue
	r.queue = nil
	var running []*run
	for _, rn := range r.runs {
		running = append(running, rn)
	}
	r.mu.Unlock()
	for _, rn := range queued {
		r.finishQueued(rn, store.JobInterrupted)
	}
	for _, rn := range running {
		rn.stop(2*time.Second, true)
	}
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (r *Runner) finishQueued(rn *run, status string) {
	rn.mu.Lock()
	rn.cancelled = true
	rn.mu.Unlock()
	if err := r.store.FinishJob(context.Background(), rn.job.ID, status, nil, 0); err != nil {
		r.log.Error("finish job", "job", rn.job.ID, "err", err)
	}
	rn.end(status)
	r.mu.Lock()
	delete(r.runs, rn.job.ID)
	r.mu.Unlock()
}

func (r *Runner) execute(rn *run) {
	defer r.wg.Done()
	ctx := context.Background()
	id := rn.job.ID
	status, exit := r.runProcess(ctx, rn)

	lines := rn.closeLog()
	if err := r.store.FinishJob(ctx, id, status, exit, lines); err != nil {
		r.log.Error("finish job", "job", id, "err", err)
	}
	r.log.Info("job finished", "job", id, "project", rn.job.ProjectID, "status", status, "lines", lines)
	rn.end(status)

	r.mu.Lock()
	delete(r.runs, id)
	delete(r.busy, rn.job.ProjectID)
	r.active--
	r.dispatchLocked()
	r.mu.Unlock()

	if r.keepJobs > 0 {
		paths, err := r.store.PruneJobs(ctx, rn.job.ProjectID, r.keepJobs)
		if err != nil {
			r.log.Warn("prune jobs", "err", err)
		}
		for _, p := range paths {
			os.Remove(p)
		}
	}
}

func (r *Runner) runProcess(ctx context.Context, rn *run) (status string, exit *int) {
	id := rn.job.ID
	if err := rn.openLog(rn.job.LogPath); err != nil {
		r.log.Error("open job log", "job", id, "err", err)
		return store.JobFailed, nil
	}
	if err := r.store.StartJob(ctx, id, gitHead(rn.cmd.Dir)); err != nil {
		r.log.Error("start job", "job", id, "err", err)
	}
	rn.publishStatus(store.JobRunning)
	r.log.Info("job started", "job", id, "project", rn.job.ProjectID, "argv", rn.cmd.Argv)

	pr, pw, err := os.Pipe()
	if err != nil {
		rn.writeLine("[buildyard] " + err.Error())
		return store.JobFailed, nil
	}
	cmd := exec.Command(rn.cmd.Argv[0], rn.cmd.Argv[1:]...)
	cmd.Dir, cmd.Env = rn.cmd.Dir, rn.cmd.Env
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = pw, pw
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	rn.mu.Lock()
	if rn.cancelled { // cancelled between dispatch and start
		rn.mu.Unlock()
		pr.Close()
		pw.Close()
		return store.JobCancelled, nil
	}
	err = cmd.Start()
	if err == nil {
		rn.proc = cmd.Process
	}
	rn.mu.Unlock()
	pw.Close() // the child holds its own copy
	if err != nil {
		pr.Close()
		rn.writeLine("[buildyard] cannot start " + rn.cmd.Argv[0] + ": " + err.Error())
		return store.JobFailed, nil
	}

	copied := make(chan struct{})
	go func() {
		defer close(copied)
		br := bufio.NewReaderSize(pr, 64*1024)
		for {
			line, err := br.ReadString('\n')
			if line != "" {
				rn.writeLine(strings.TrimRight(line, "\r\n"))
			}
			if err != nil {
				return
			}
		}
	}()

	werr := cmd.Wait()
	// Background processes that kept the pipe open (e.g. a daemon the build spawned) must
	// not hold the job: give the reader a moment, then cut the pipe.
	select {
	case <-copied:
	case <-time.After(2 * time.Second):
	}
	pr.Close()
	<-copied

	rn.mu.Lock()
	cancelled, interrupted := rn.cancelled, rn.interrupted
	rn.mu.Unlock()
	code := cmd.ProcessState.ExitCode()
	switch {
	case interrupted:
		return store.JobInterrupted, &code
	case cancelled:
		return store.JobCancelled, &code
	case werr == nil:
		return store.JobSucceeded, &code
	default:
		return store.JobFailed, &code
	}
}

func gitHead(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ReadLines returns lines [from, from+limit) of a log file (1-based), and the total count.
func ReadLines(path string, from, limit int) ([]string, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	if from < 1 {
		from = 1
	}
	br := bufio.NewReaderSize(f, 64*1024)
	var out []string
	n := 0
	for {
		line, err := br.ReadString('\n')
		if line != "" && strings.HasSuffix(line, "\n") {
			n++
			if n >= from && (limit <= 0 || len(out) < limit) {
				out = append(out, strings.TrimSuffix(line, "\n"))
			}
		}
		if err == io.EOF {
			return out, n, nil
		}
		if err != nil {
			return out, n, err
		}
	}
}
