package runner

import (
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/binarycat0/buildyard/internal/store"
)

// Event is one item on a job's live bus.
type Event struct {
	Type   string `json:"type"`             // line | status
	N      int    `json:"n,omitempty"`      // line number, 1-based
	Text   string `json:"text,omitempty"`   // line text
	Status string `json:"status,omitempty"` // for status events
}

// Sub is a live subscription to one job.
type Sub struct {
	C    <-chan Event
	From int // lines 1..From are in the log file; C starts at From+1
	c    chan Event
	run  *run
	// Dropped is set when the subscriber fell behind and C was closed early.
	Dropped bool
}

// Close detaches the subscriber.
func (s *Sub) Close() {
	s.run.mu.Lock()
	defer s.run.mu.Unlock()
	if _, ok := s.run.subs[s]; ok {
		delete(s.run.subs, s)
		close(s.c)
	}
}

const subBuffer = 8192

type run struct {
	job store.Job
	cmd Command

	mu          sync.Mutex
	file        *os.File
	lines       int
	subs        map[*Sub]struct{}
	proc        *os.Process
	cancelled   bool
	interrupted bool
	ended       bool
	done        chan struct{}
}

func (rn *run) openLog(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	rn.mu.Lock()
	rn.file = f
	rn.mu.Unlock()
	return nil
}

// writeLine appends to the file and publishes under one lock, so a subscriber sees
// every line exactly once: either in the file (<= From) or on its channel.
func (rn *run) writeLine(text string) {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	if rn.file != nil {
		rn.file.WriteString(text + "\n")
	}
	rn.lines++
	rn.publishLocked(Event{Type: "line", N: rn.lines, Text: text})
}

func (rn *run) publishStatus(status string) {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	rn.publishLocked(Event{Type: "status", Status: status})
}

func (rn *run) publishLocked(ev Event) {
	for s := range rn.subs {
		select {
		case s.c <- ev:
		default: // too slow: drop it; the browser reconnects with Last-Event-ID
			s.Dropped = true
			delete(rn.subs, s)
			close(s.c)
		}
	}
}

func (rn *run) subscribe() *Sub {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	c := make(chan Event, subBuffer)
	s := &Sub{C: c, c: c, From: rn.lines, run: rn}
	if rn.ended {
		close(c)
		return s
	}
	rn.subs[s] = struct{}{}
	return s
}

func (rn *run) closeLog() int {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	if rn.file != nil {
		rn.file.Close()
		rn.file = nil
	}
	return rn.lines
}

// end publishes the final status and closes all subscribers.
func (rn *run) end(status string) {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	rn.publishLocked(Event{Type: "status", Status: status})
	for s := range rn.subs {
		delete(rn.subs, s)
		close(s.c)
	}
	if !rn.ended {
		rn.ended = true
		close(rn.done)
	}
}

// stop cancels a running job: SIGTERM to the process group, SIGKILL after grace.
func (rn *run) stop(grace time.Duration, interrupted bool) {
	rn.mu.Lock()
	rn.cancelled = true
	if interrupted {
		rn.interrupted = true
	}
	proc, leaderOnly, ended := rn.proc, rn.cmd.SignalLeaderOnly, rn.ended
	rn.mu.Unlock()
	if proc == nil || ended {
		return // not started yet; runProcess sees the flag
	}
	pgid := proc.Pid
	if leaderOnly {
		proc.Signal(syscall.SIGINT)
	} else {
		syscall.Kill(-pgid, syscall.SIGTERM)
	}
	go func() {
		select {
		case <-rn.done:
			if !leaderOnly {
				// The leader is gone; take down anything in the group that ignored SIGTERM.
				syscall.Kill(-pgid, syscall.SIGKILL)
			}
		case <-time.After(grace):
			if leaderOnly {
				proc.Kill()
			} else {
				syscall.Kill(-pgid, syscall.SIGKILL)
			}
		}
	}()
}
