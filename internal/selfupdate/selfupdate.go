// Package selfupdate replaces the running executable with a staged file. It
// stages in the target's directory so the final move never crosses file
// systems, and never escalates privileges.
package selfupdate

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

// ErrNotWritable reports that the target's directory cannot hold a staged file.
var ErrNotWritable = errors.New("directory is not writable")

// Staged is a temporary file next to the target, waiting to replace it.
type Staged struct {
	file   *os.File
	target string
	mode   os.FileMode
	done   bool
}

// Resolve returns the real file behind an executable path, following symlinks.
func Resolve(path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("resolve %s: %s is a directory", path, real)
	}
	return real, nil
}

// Stage checks that target exists and that its directory is writable by
// creating a temporary file there, which stays open for the caller to fill.
func Stage(target string) (*Staged, error) {
	info, err := os.Stat(target)
	if err != nil {
		return nil, fmt.Errorf("stage %s: %w", target, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("stage %s: is a directory", target)
	}
	dir := filepath.Dir(target)
	f, err := os.CreateTemp(dir, "."+filepath.Base(target)+".update-*")
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrNotWritable, dir, err)
	}
	mode := info.Mode().Perm()
	if mode&0o111 == 0 {
		mode = 0o755
	}
	return &Staged{file: f, target: target, mode: mode}, nil
}

// Writer returns the temporary file for the caller to fill.
func (s *Staged) Writer() io.Writer {
	return s.file
}

// Commit closes the staged file, applies the target's permission bits, and
// replaces the target. On failure the staged file is removed.
func (s *Staged) Commit() error {
	if s.done {
		return errors.New("commit: staged file already finished")
	}
	s.done = true
	name := s.file.Name()
	if err := s.file.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("commit %s: %w", s.target, err)
	}
	if err := os.Chmod(name, s.mode); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("commit %s: %w", s.target, err)
	}
	var err error
	if runtime.GOOS == "windows" {
		err = replaceAside(s.target, name)
	} else {
		err = os.Rename(name, s.target)
	}
	if err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("commit %s: %w", s.target, err)
	}
	return nil
}

// Abort closes and removes the staged file. It is a no-op after Commit and
// when called repeatedly.
func (s *Staged) Abort() {
	if s.done {
		return
	}
	s.done = true
	_ = s.file.Close()
	_ = os.Remove(s.file.Name())
}

// CleanupOld removes a leftover <target>.old, ignoring errors.
func CleanupOld(target string) {
	_ = os.Remove(target + ".old")
}

// replaceAside moves target to <target>.old, then staged into place, and
// restores the original when the second move fails.
func replaceAside(target, staged string) error {
	old := target + ".old"
	if err := os.Remove(old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(target, old); err != nil {
		return err
	}
	if err := os.Rename(staged, target); err != nil {
		if restoreErr := os.Rename(old, target); restoreErr != nil {
			return errors.Join(err, restoreErr)
		}
		return err
	}
	return nil
}
