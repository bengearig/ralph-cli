//go:build windows

package loop

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockRegion locks one byte far past the file's content, so other processes
// can still read the holder's description.
func lockRegion() *windows.Overlapped { return &windows.Overlapped{OffsetHigh: 1} }

func lockFile(f *os.File) error {
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, lockRegion())
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errLocked
	}
	return err
}

func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, lockRegion())
}
