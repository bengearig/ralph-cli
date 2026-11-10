// Package looplock is the project-wide lock that keeps one ralph loop running
// per project, and a probe other commands use to see who holds it.
package looplock

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// ErrLoopRunning means another ralph loop holds the project's lock.
var ErrLoopRunning = errors.New("another ralph loop is running in this project")

var errLocked = errors.New("locked")

// Lock is an exclusive, non-blocking lock on a file. The operating
// system releases it if the process dies.
type Lock struct {
	f *os.File
}

// Acquire takes the lock at path without blocking and records owner as its holder.
func Acquire(path, owner string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening loop lock: %w", err)
	}
	if err := lockFile(f); err != nil {
		holder, _ := os.ReadFile(path)
		f.Close()
		if errors.Is(err, errLocked) {
			if h := strings.TrimSpace(string(holder)); h != "" {
				return nil, fmt.Errorf("%w (%s)", ErrLoopRunning, h)
			}
			return nil, ErrLoopRunning
		}
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	info := fmt.Sprintf("pid %d, %s, started %s\n", os.Getpid(), owner, time.Now().Format(time.RFC3339))
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(info), 0)
	}
	return &Lock{f: f}, nil
}

// Release drops the lock.
func (l *Lock) Release() error {
	_ = l.f.Truncate(0)
	return errors.Join(unlockFile(l.f), l.f.Close())
}

// Holder describes the process holding a lock. Fields are empty when the
// holder file could not be parsed.
type Holder struct {
	PID       int
	Milestone string
	Started   time.Time
}

// Probe reports whether the lock at path is held, without keeping it. A
// missing file means not held.
func Probe(path string) (Holder, bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if errors.Is(err, os.ErrNotExist) {
		return Holder{}, false, nil
	}
	if err != nil {
		return Holder{}, false, fmt.Errorf("opening loop lock: %w", err)
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		if errors.Is(err, errLocked) {
			data, _ := os.ReadFile(path)
			return parseHolder(string(data)), true, nil
		}
		return Holder{}, false, fmt.Errorf("probing %s: %w", path, err)
	}
	return Holder{}, false, unlockFile(f)
}

// parseHolder reads "pid <n>, milestone <M-n>, started <RFC3339>".
func parseHolder(s string) Holder {
	parts := strings.Split(strings.TrimSpace(s), ", ")
	if len(parts) != 3 {
		return Holder{}
	}
	pid, ok1 := strings.CutPrefix(parts[0], "pid ")
	ms, ok2 := strings.CutPrefix(parts[1], "milestone ")
	st, ok3 := strings.CutPrefix(parts[2], "started ")
	if !ok1 || !ok2 || !ok3 {
		return Holder{}
	}
	n, err := strconv.Atoi(pid)
	if err != nil {
		return Holder{}
	}
	t, err := time.Parse(time.RFC3339, st)
	if err != nil {
		return Holder{}
	}
	return Holder{PID: n, Milestone: ms, Started: t}
}
