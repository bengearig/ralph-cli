//go:build unix

package claudecode

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// configureProcess runs the session in its own process group so that
// stopping it also stops everything it started, such as MCP servers and
// shell commands. Stopping sends SIGINT, then SIGKILL after grace. The
// returned func must be called once the process has exited: it kills
// anything the session left running and cancels a pending SIGKILL.
func configureProcess(cmd *exec.Cmd, grace time.Duration) (exited func()) {
	var mu sync.Mutex
	var timer *time.Timer
	done := false
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		pgid := cmd.Process.Pid
		err := syscall.Kill(-pgid, syscall.SIGINT)
		mu.Lock()
		if !done {
			timer = time.AfterFunc(grace, func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
		}
		mu.Unlock()
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return func() {
		mu.Lock()
		defer mu.Unlock()
		done = true
		if timer != nil {
			timer.Stop()
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
