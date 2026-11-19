package tasks

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSubmitRunsInBackgroundAndDedups(t *testing.T) {
	m := New(2, nil)
	defer m.Close()
	release := make(chan struct{})
	t1, err := m.Submit("jdk.detect", "", func(ctx context.Context) (any, error) {
		<-release
		return 3, nil
	})
	if err != nil || t1.Status != Queued {
		t.Fatalf("submit: %+v %v", t1, err)
	}
	// Same key while running → same task, the second func never runs.
	t2, _ := m.Submit("jdk.detect", "", func(ctx context.Context) (any, error) {
		t.Error("duplicate task ran")
		return nil, nil
	})
	if t2.ID != t1.ID {
		t.Fatalf("dedup: got %d, want %d", t2.ID, t1.ID)
	}
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done, err := m.Wait(ctx, t1.ID)
	if err != nil || done.Status != Succeeded || done.Result != 3 || done.FinishedAt == nil {
		t.Fatalf("wait: %+v %v", done, err)
	}
	// After it finished, the key is free again.
	t3, _ := m.Submit("jdk.detect", "", func(ctx context.Context) (any, error) { return nil, nil })
	if t3.ID == t1.ID {
		t.Fatal("finished task reused")
	}
}

func TestFailureAndPanicAreRecorded(t *testing.T) {
	m := New(1, nil)
	defer m.Close()
	ctx := context.Background()
	a, _ := m.Submit("x", "a", func(context.Context) (any, error) { return nil, errors.New("boom") })
	b, _ := m.Submit("x", "b", func(context.Context) (any, error) { panic("oops") })
	if got, _ := m.Wait(ctx, a.ID); got.Status != Failed || got.Error != "boom" {
		t.Fatalf("a: %+v", got)
	}
	if got, _ := m.Wait(ctx, b.ID); got.Status != Failed || got.Error != "panic: oops" {
		t.Fatalf("b: %+v", got)
	}
	if l := m.List(); len(l) != 2 || l[0].ID != b.ID {
		t.Fatalf("list: %+v", l)
	}
}

func TestCloseCancelsRunningTask(t *testing.T) {
	m := New(1, nil)
	started := make(chan struct{})
	task, _ := m.Submit("x", "", func(ctx context.Context) (any, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	<-started
	m.Close()
	if got, _ := m.Get(task.ID); got.Status != Failed {
		t.Fatalf("after close: %+v", got)
	}
	if _, err := m.Submit("x", "", nil); err == nil {
		t.Fatal("submit after close should fail")
	}
}
