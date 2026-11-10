package looplock

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLockReleasedForNextHolder(t *testing.T) {
	p := filepath.Join(t.TempDir(), "loop.lock")
	a, err := Acquire(p, "first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(p, "second"); !errors.Is(err, ErrLoopRunning) || !strings.Contains(err.Error(), "first") {
		t.Errorf("contention = %v", err)
	}
	a.Release()
	b, err := Acquire(p, "second")
	if err != nil {
		t.Fatal(err)
	}
	b.Release()
}

func TestProbeMissingFile(t *testing.T) {
	_, held, err := Probe(filepath.Join(t.TempDir(), "loop.lock"))
	if err != nil || held {
		t.Errorf("held = %v, err = %v", held, err)
	}
}

func TestProbeFreeLock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "loop.lock")
	l, err := Acquire(p, "milestone M-1")
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
	if _, held, err := Probe(p); err != nil || held {
		t.Errorf("held = %v, err = %v", held, err)
	}
	again, err := Acquire(p, "milestone M-2")
	if err != nil {
		t.Fatalf("Probe left the lock held: %v", err)
	}
	again.Release()
}

func TestProbeHeld(t *testing.T) {
	p := filepath.Join(t.TempDir(), "loop.lock")
	l, err := Acquire(p, "milestone M-7")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	h, held, err := Probe(p)
	if err != nil {
		t.Fatal(err)
	}
	if !held {
		t.Skip("locking is a no-op on this platform")
	}
	if h.PID != os.Getpid() || h.Milestone != "M-7" || h.Started.IsZero() {
		t.Errorf("holder = %+v", h)
	}
}

func TestProbeUnparsableHolder(t *testing.T) {
	p := filepath.Join(t.TempDir(), "loop.lock")
	l, err := Acquire(p, "milestone M-7")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if err := os.WriteFile(p, []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, held, err := Probe(p)
	if err != nil {
		t.Fatal(err)
	}
	if !held {
		t.Skip("locking is a no-op on this platform")
	}
	if h != (Holder{}) {
		t.Errorf("holder = %+v, want empty", h)
	}
}
