//go:build unix

package lock

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestSecondAcquireFailsUntilReleased(t *testing.T) {
	p := filepath.Join(t.TempDir(), "daemon.lock")
	l, err := Acquire(p)
	if err != nil {
		t.Fatal(err)
	}
	// flock locks are per open file description, so a second open in the same
	// process conflicts just like a second daemon would.
	if _, err := Acquire(p); !errors.Is(err, ErrHeld) {
		t.Fatalf("second acquire: got %v, want ErrHeld", err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	l2, err := Acquire(p)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	l2.Release()
}
