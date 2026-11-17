// Package tasks runs background work (scans, detection, syncs) on worker goroutines.
// A request enqueues a task and gets its id back immediately; clients poll GET /api/tasks/{id}.
// Builds are not tasks: they go through the runner (phase 2), which has its own queue and logs.
package tasks

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// Status of a task.
const (
	Queued    = "queued"
	Running   = "running"
	Succeeded = "succeeded"
	Failed    = "failed"
)

// Task is a snapshot of one unit of background work.
type Task struct {
	ID         int64      `json:"id"`
	Kind       string     `json:"kind"` // e.g. "jdk.detect"
	Key        string     `json:"key"`  // dedup key: one active task per key
	Status     string     `json:"status"`
	Error      string     `json:"error,omitempty"`
	Result     any        `json:"result,omitempty"`
	QueuedAt   time.Time  `json:"queuedAt"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// Func does the work. Its result is stored on the task.
type Func func(ctx context.Context) (any, error)

type entry struct {
	task Task
	fn   Func
	done chan struct{}
}

// Manager owns the queue and the workers.
type Manager struct {
	mu      sync.Mutex
	next    int64
	tasks   map[int64]*entry
	active  map[string]int64 // key → id of the queued or running task
	queue   chan *entry
	keep    int // finished tasks kept in memory
	log     *slog.Logger
	wg      sync.WaitGroup
	cancel  context.CancelFunc
	closing bool
}

// New starts a manager with n workers. Stop it with Close.
func New(workers int, log *slog.Logger) *Manager {
	if workers < 1 {
		workers = 1
	}
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		tasks:  map[int64]*entry{},
		active: map[string]int64{},
		queue:  make(chan *entry, 256),
		keep:   200,
		log:    log,
		cancel: cancel,
	}
	for i := 0; i < workers; i++ {
		m.wg.Add(1)
		go m.worker(ctx)
	}
	return m
}

// Submit enqueues fn. If a task with the same key is still queued or running, that task is
// returned instead and fn is dropped, so repeated clicks do not pile up duplicate scans.
func (m *Manager) Submit(kind, key string, fn Func) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return Task{}, fmt.Errorf("task manager is shutting down")
	}
	if key == "" {
		key = kind
	}
	if id, ok := m.active[key]; ok {
		return m.tasks[id].task, nil
	}
	m.next++
	e := &entry{
		task: Task{ID: m.next, Kind: kind, Key: key, Status: Queued, QueuedAt: time.Now().UTC()},
		fn:   fn,
		done: make(chan struct{}),
	}
	select {
	case m.queue <- e:
	default:
		m.next--
		return Task{}, fmt.Errorf("task queue is full")
	}
	m.tasks[e.task.ID] = e
	m.active[key] = e.task.ID
	m.gc()
	return e.task, nil
}

// Get returns a task snapshot.
func (m *Manager) Get(id int64) (Task, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.tasks[id]
	if !ok {
		return Task{}, false
	}
	return e.task, true
}

// List returns task snapshots, newest first.
func (m *Manager) List() []Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Task, 0, len(m.tasks))
	for _, e := range m.tasks {
		out = append(out, e.task)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// Wait blocks until the task finishes or ctx is done, then returns its snapshot.
func (m *Manager) Wait(ctx context.Context, id int64) (Task, error) {
	m.mu.Lock()
	e, ok := m.tasks[id]
	m.mu.Unlock()
	if !ok {
		return Task{}, fmt.Errorf("task %d not found", id)
	}
	select {
	case <-e.done:
	case <-ctx.Done():
		return Task{}, ctx.Err()
	}
	t, _ := m.Get(id)
	return t, nil
}

// Close stops accepting tasks, cancels running ones and waits for the workers.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return
	}
	m.closing = true
	close(m.queue)
	m.mu.Unlock()
	m.cancel()
	m.wg.Wait()
}

func (m *Manager) worker(ctx context.Context) {
	defer m.wg.Done()
	for e := range m.queue {
		m.run(ctx, e)
	}
}

func (m *Manager) run(ctx context.Context, e *entry) {
	start := time.Now().UTC()
	m.mu.Lock()
	e.task.Status, e.task.StartedAt = Running, &start
	m.mu.Unlock()

	result, err := safeCall(ctx, e.fn)

	end := time.Now().UTC()
	m.mu.Lock()
	e.task.FinishedAt = &end
	if err != nil {
		e.task.Status, e.task.Error = Failed, err.Error()
	} else {
		e.task.Status, e.task.Result = Succeeded, result
	}
	delete(m.active, e.task.Key)
	m.mu.Unlock()
	close(e.done)
	m.log.Info("task finished", "id", e.task.ID, "kind", e.task.Kind, "status", e.task.Status,
		"ms", end.Sub(start).Milliseconds(), "err", e.task.Error)
}

func safeCall(ctx context.Context, fn Func) (res any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return fn(ctx)
}

// gc drops the oldest finished tasks beyond the retention limit. Caller holds mu.
func (m *Manager) gc() {
	if len(m.tasks) <= m.keep {
		return
	}
	var finished []int64
	for id, e := range m.tasks {
		if e.task.FinishedAt != nil {
			finished = append(finished, id)
		}
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i] < finished[j] })
	for _, id := range finished {
		if len(m.tasks) <= m.keep {
			break
		}
		delete(m.tasks, id)
	}
}
