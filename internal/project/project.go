// Package project locates a ralph project on disk.
package project

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	// DirName is the per-project directory holding config and the database.
	DirName = ".ralph"
	// ConfigFileName is the project config file inside DirName.
	ConfigFileName = "config.yaml"
)

// ErrNotFound means no ancestor directory contains a .ralph directory.
var ErrNotFound = errors.New("no ralph project found; run `ralph init` in your repository")

// Project is a directory tree managed by ralph.
type Project struct {
	Root string
}

// Dir is the project's .ralph directory.
func (p Project) Dir() string { return filepath.Join(p.Root, DirName) }

// ConfigPath is the project config file.
func (p Project) ConfigPath() string { return filepath.Join(p.Dir(), ConfigFileName) }

// Locate walks up from start to the nearest directory containing .ralph/.
func Locate(start string) (Project, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return Project{}, err
	}
	for {
		if isDir(filepath.Join(dir, DirName)) {
			return Project{Root: dir}, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return Project{}, ErrNotFound
		}
		dir = parent
	}
}

// At returns the project rooted exactly at dir, requiring .ralph/ to exist.
func At(dir string) (Project, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Project{}, err
	}
	if !isDir(filepath.Join(abs, DirName)) {
		return Project{}, fmt.Errorf("%s: %w", abs, ErrNotFound)
	}
	return Project{Root: abs}, nil
}

// GitRoot returns the top-level directory of the git work tree containing
// dir. It returns an error wrapping ErrNotGitRepo when dir is not in one.
func GitRoot(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("%w: %s", ErrNotGitRepo, strings.TrimSpace(stderr.String()))
		}
		return "", fmt.Errorf("running git: %w", err)
	}
	return filepath.Clean(strings.TrimSpace(string(out))), nil
}

// ErrNotGitRepo means the directory is not inside a git work tree.
var ErrNotGitRepo = errors.New("not a git repository")

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
