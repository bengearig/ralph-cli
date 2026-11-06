package loop

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// ErrLoopRunning means another ralph loop holds the project's lock.
var ErrLoopRunning = errors.New("another ralph loop is running in this project")

var errLocked = errors.New("locked")

// projectLock is an exclusive, non-blocking lock on a file. The operating
// system releases it if the process dies.
type projectLock struct {
	f *os.File
}

func acquireLock(path, owner string) (*projectLock, error) {
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
	return &projectLock{f: f}, nil
}

func (l *projectLock) release() error {
	_ = l.f.Truncate(0)
	return errors.Join(unlockFile(l.f), l.f.Close())
}
