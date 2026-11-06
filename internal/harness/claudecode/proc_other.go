//go:build !unix

package claudecode

import (
	"os/exec"
	"time"
)

// configureProcess kills the session outright; there is no portable
// interrupt or process group outside Unix, so leftovers are not cleaned up.
func configureProcess(cmd *exec.Cmd, _ time.Duration) (exited func()) {
	cmd.Cancel = func() error { return cmd.Process.Kill() }
	return func() {}
}
