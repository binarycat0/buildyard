//go:build unix

// Package lock keeps a second daemon from starting, using flock on daemon.lock.
package lock

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// ErrHeld is returned when another process holds the lock.
var ErrHeld = errors.New("buildyard is already running")

// Lock is a held daemon lock. Release it on shutdown.
type Lock struct{ f *os.File }

// Acquire takes an exclusive, non-blocking flock on path and writes our PID into it.
// The lock is released by the OS if the process dies, so a stale file never blocks a restart.
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		pid := readPID(f)
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			if pid > 0 {
				return nil, fmt.Errorf("%w (pid %d)", ErrHeld, pid)
			}
			return nil, ErrHeld
		}
		return nil, err
	}
	if err := f.Truncate(0); err == nil {
		f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return &Lock{f: f}, nil
}

func readPID(f *os.File) int {
	buf := make([]byte, 32)
	n, _ := f.ReadAt(buf, 0)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	return pid
}

// Release unlocks and closes the lock file.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	err := l.f.Close()
	l.f = nil
	return err
}
