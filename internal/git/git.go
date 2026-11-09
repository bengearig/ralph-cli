// Package git runs the git commands the loop needs.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Repo is a git work tree.
type Repo struct {
	// Dir is the work-tree root.
	Dir string
	// Exclude is a path, relative to Dir, that ralph never stages or counts
	// as a change: its own .ralph directory.
	Exclude string
}

// run executes git and returns trimmed stdout. Failures include git's stderr.
func (r Repo) run(ctx context.Context, args ...string) (string, error) {
	out, err := r.runRaw(ctx, args...)
	return strings.TrimSpace(out), err
}

// runRaw executes git and returns stdout unmodified.
func (r Repo) runRaw(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", &Error{Args: args, Msg: msg, Err: err}
	}
	return stdout.String(), nil
}

// Error is a failed git command.
type Error struct {
	Args []string
	Msg  string
	Err  error
}

func (e *Error) Error() string { return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), e.Msg) }
func (e *Error) Unwrap() error { return e.Err }

// exitCode returns the exit status of a failed git command, or -1.
func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func (r Repo) pathspec() []string {
	ps := []string{"--", "."}
	if r.Exclude != "" {
		ps = append(ps, ":(exclude)"+r.Exclude)
	}
	return ps
}

// CurrentBranch returns the checked-out branch, or "" when HEAD is detached.
func (r Repo) CurrentBranch(ctx context.Context) (string, error) {
	out, err := r.run(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		if exitCode(err) == 1 {
			return "", nil
		}
		return "", err
	}
	return out, nil
}

// RefExists reports whether ref resolves to a commit.
func (r Repo) RefExists(ctx context.Context, ref string) (bool, error) {
	_, err := r.run(ctx, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		if exitCode(err) == 1 {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// RevParse returns the full object name of the commit ref points to.
func (r Repo) RevParse(ctx context.Context, ref string) (string, error) {
	return r.run(ctx, "rev-parse", "--verify", ref+"^{commit}")
}

// MergeBase returns the full object name of a best common ancestor of a and
// b, or found=false when they share no history.
func (r Repo) MergeBase(ctx context.Context, a, b string) (hash string, found bool, err error) {
	out, err := r.run(ctx, "merge-base", a, b)
	if err != nil {
		if exitCode(err) == 1 {
			return "", false, nil
		}
		return "", false, err
	}
	return out, true, nil
}

// BranchExists reports whether a local branch exists.
func (r Repo) BranchExists(ctx context.Context, name string) (bool, error) {
	return r.RefExists(ctx, "refs/heads/"+name)
}

// Dirty lists changed and untracked paths outside Exclude.
func (r Repo) Dirty(ctx context.Context) ([]string, error) {
	args := append([]string{"status", "--porcelain=v1", "-z", "--untracked-files=all"}, r.pathspec()...)
	out, err := r.runRaw(ctx, args...)
	if err != nil {
		return nil, err
	}
	var paths []string
	entries := strings.Split(out, "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		paths = append(paths, e[3:])
		// Renames and copies are followed by their original path.
		if e[0] == 'R' || e[0] == 'C' {
			i++
		}
	}
	return paths, nil
}

// Switch checks out an existing branch.
func (r Repo) Switch(ctx context.Context, branch string) error {
	_, err := r.run(ctx, "switch", branch)
	return err
}

// CreateBranch creates and checks out branch at start without tracking it,
// so a later push does not target start's upstream.
func (r Repo) CreateBranch(ctx context.Context, branch, start string) error {
	_, err := r.run(ctx, "switch", "--no-track", "-c", branch, start)
	return err
}

// CommitAll stages every change outside Exclude and commits it. It returns
// committed=false when there was nothing to commit. Commit hooks run.
func (r Repo) CommitAll(ctx context.Context, message string) (hash string, committed bool, err error) {
	if _, err := r.run(ctx, append([]string{"add", "--all"}, r.pathspec()...)...); err != nil {
		return "", false, err
	}
	if _, err := r.run(ctx, "diff", "--cached", "--quiet"); err == nil {
		return "", false, nil
	} else if exitCode(err) != 1 {
		return "", false, err
	}
	if _, err := r.run(ctx, "commit", "--quiet", "-m", message); err != nil {
		return "", false, err
	}
	hash, err = r.run(ctx, "rev-parse", "--short", "HEAD")
	return hash, err == nil, err
}

// Stash saves every change outside Exclude, including untracked files, as a
// stash entry with message. It returns false when there was nothing to save.
func (r Repo) Stash(ctx context.Context, message string) (bool, error) {
	dirty, err := r.Dirty(ctx)
	if err != nil || len(dirty) == 0 {
		return false, err
	}
	args := append([]string{"stash", "push", "--quiet", "--include-untracked", "-m", message}, r.pathspec()...)
	if _, err := r.run(ctx, args...); err != nil {
		return false, err
	}
	return true, nil
}
