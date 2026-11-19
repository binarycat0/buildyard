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
	Type   string `json:"type"`             // line | status | module | modules
	N      int    `json:"n,omitempty"`      // line number, 1-based
	Text   string `json:"text,omitempty"`   // line text
	Status string `json:"status,omitempty"` // for status events
	// Module is one changed module (module events); Modules the whole list (modules
	// events, sent when the reactor becomes known).
	Module  *store.JobModule  `json:"module,omitempty"`
	Modules []store.JobModule `json:"modules,omitempty"`
	Problem *store.JobProblem `json:"problem,omitempty"`
}

// Sub is a live subscription to one job.
type Sub struct {
	C    <-chan Event
	From int // lines 1..From are in the log file; C starts at From+1
	// Modules is the per-module progress at subscription time; C carries later changes.
	Modules []store.JobModule
	// Problems found up to subscription time; C carries later ones.
	Problems []store.JobProblem
	c        chan Event
	run      *run
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
	stopAt      time.Time // when a stop was first requested
	reaper      bool
	done        chan struct{}
	prog        progress
	parser      OutputParser
	problems    []store.JobProblem
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
	if rn.parser != nil {
		probs, changed := rn.parser.Line(rn.lines, text)
		for i := range probs {
			if len(rn.problems) >= maxProblems {
				break
			}
			rn.problems = append(rn.problems, probs[i])
			rn.publishLocked(Event{Type: "problem", Problem: &probs[i]})
		}
		if changed {
			rn.syncLogModulesLocked()
		}
	}
}

func (rn *run) problemsSnapshot() []store.JobProblem {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	return append([]store.JobProblem(nil), rn.problems...)
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
	s := &Sub{C: c, c: c, From: rn.lines, Modules: rn.prog.snapshot(), Problems: append([]store.JobProblem(nil), rn.problems...), run: rn}
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

// signal sends sig to the job's process group (or only to the leader for mvnd, whose
// long-lived daemon must survive). It reports whether the process had started.
func (rn *run) signal(sig syscall.Signal, interrupted bool) bool {
	rn.mu.Lock()
	rn.cancelled = true
	if interrupted {
		rn.interrupted = true
	}
	if rn.stopAt.IsZero() {
		rn.stopAt = time.Now()
	}
	proc, leaderOnly, ended := rn.proc, rn.cmd.SignalLeaderOnly, rn.ended
	if !rn.reaper && proc != nil && !leaderOnly {
		// Once the leader is gone, take down anything left in its group.
		rn.reaper = true
		go func() {
			<-rn.done
			syscall.Kill(-proc.Pid, syscall.SIGKILL)
		}()
	}
	rn.mu.Unlock()
	if proc == nil || ended {
		return false // not started yet; runProcess sees the flag
	}
	if leaderOnly {
		proc.Signal(sig)
	} else {
		syscall.Kill(-proc.Pid, sig)
	}
	return true
}

// stop asks the job to finish (SIGTERM) and kills it after grace.
func (rn *run) stop(grace time.Duration, interrupted bool) {
	if !rn.signal(syscall.SIGTERM, interrupted) {
		return
	}
	go func() {
		select {
		case <-rn.done:
		case <-time.After(grace):
			rn.signal(syscall.SIGKILL, interrupted)
		}
	}()
}
