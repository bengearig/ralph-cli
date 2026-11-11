// Package git runs the git commands the loop needs.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
	return r.runInput(ctx, nil, args...)
}

// runInput executes git with stdin and returns stdout unmodified.
func (r Repo) runInput(ctx context.Context, stdin io.Reader, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.Dir
	cmd.Stdin = stdin
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

// HeadState returns the checked-out branch, empty when HEAD is detached, and
// the full commit HEAD points to.
func (r Repo) HeadState(ctx context.Context) (branch, commit string, err error) {
	if branch, err = r.CurrentBranch(ctx); err != nil {
		return "", "", err
	}
	if commit, err = r.RevParse(ctx, "HEAD"); err != nil {
		return "", "", err
	}
	return branch, commit, nil
}

// MergeFFOnly fast-forwards the checked-out branch to ref, failing without
// changes when that would need a merge commit.
func (r Repo) MergeFFOnly(ctx context.Context, ref string) error {
	_, err := r.run(ctx, "merge", "--ff-only", "--quiet", ref)
	return err
}

// ResetHard moves the checked-out branch, the index, and the work tree to ref.
func (r Repo) ResetHard(ctx context.Context, ref string) error {
	_, err := r.run(ctx, "reset", "--hard", "--quiet", ref)
	return err
}

// DeleteBranch deletes a local branch with the safe delete, which refuses a
// branch that is not fully merged.
func (r Repo) DeleteBranch(ctx context.Context, name string) error {
	_, err := r.run(ctx, "branch", "-d", "--", name)
	return err
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

// IsAncestor reports whether ancestor is reachable from descendant. A commit
// is an ancestor of itself.
func (r Repo) IsAncestor(ctx context.Context, ancestor, descendant string) (bool, error) {
	_, err := r.run(ctx, "merge-base", "--is-ancestor", ancestor, descendant)
	if err != nil {
		if exitCode(err) == 1 {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// CommitExists reports whether hash names a commit present in the object
// database. Missing objects and objects of another type yield false.
func (r Repo) CommitExists(ctx context.Context, hash string) (bool, error) {
	if strings.ContainsAny(hash, "\n\r") {
		return false, fmt.Errorf("git: invalid object name %q", hash)
	}
	// --batch-check reports a missing object on stdout, whereas cat-file -e
	// exits 128 for it, as it does for real failures.
	out, err := r.runInput(ctx, strings.NewReader(hash+"^{commit}\n"), "cat-file", "--batch-check=%(objecttype)")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "commit", nil
}

// LogCommit is one commit listed by Log.
type LogCommit struct {
	Hash    string
	Subject string
	// Trailers maps each requested trailer key to its values in message
	// order. Keys without values are absent.
	Trailers map[string][]string
}

// Log lists the commits in rangeSpec, such as "base..tip", newest first, with
// the values of each trailer in trailerKeys.
func (r Repo) Log(ctx context.Context, rangeSpec string, trailerKeys ...string) ([]LogCommit, error) {
	format := "%H%x00%s"
	for _, k := range trailerKeys {
		if k == "" || strings.ContainsAny(k, ",():%\x00\n\r\t ") {
			return nil, fmt.Errorf("git: invalid trailer key %q", k)
		}
		// Unfolded values hold no newlines, so a newline separates them.
		format += "%x00%(trailers:key=" + k + ",valueonly,unfold,separator=%x0A)"
	}
	out, err := r.runRaw(ctx, "log", "-z", "--no-show-signature", "--no-color", "--format="+format, rangeSpec, "--")
	if err != nil {
		return nil, err
	}
	commits := []LogCommit{}
	fields := strings.Split(out, "\x00")
	// Each record is NUL-terminated, leaving one empty field at the end.
	fields = fields[:len(fields)-1]
	width := 2 + len(trailerKeys)
	if len(fields)%width != 0 {
		return nil, fmt.Errorf("git log %s: unexpected output: %d fields for %d per commit", rangeSpec, len(fields), width)
	}
	for i := 0; i < len(fields); i += width {
		c := LogCommit{Hash: fields[i], Subject: fields[i+1], Trailers: map[string][]string{}}
		for j, k := range trailerKeys {
			if v := fields[i+2+j]; v != "" {
				c.Trailers[k] = strings.Split(v, "\n")
			}
		}
		commits = append(commits, c)
	}
	return commits, nil
}

// PatchID returns the stable patch-id of the commit hash's change, or "" when
// its diff is empty, as for merges and empty commits.
func (r Repo) PatchID(ctx context.Context, hash string) (string, error) {
	diff, err := r.runRaw(ctx, "show", "--no-color", "--no-ext-diff", "--no-textconv", "--diff-merges=off", "--pretty=format:", hash, "--")
	if err != nil {
		return "", err
	}
	if diff == "" {
		return "", nil
	}
	out, err := r.runInput(ctx, strings.NewReader(diff), "patch-id", "--stable")
	if err != nil {
		return "", err
	}
	id, _, _ := strings.Cut(strings.TrimSpace(out), " ")
	return id, nil
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

// SwitchDetached checks out commit with a detached HEAD.
func (r Repo) SwitchDetached(ctx context.Context, commit string) error {
	_, err := r.run(ctx, "switch", "--detach", commit)
	return err
}

// CreateBranch creates and checks out branch at start without tracking it,
// so a later push does not target start's upstream.
func (r Repo) CreateBranch(ctx context.Context, branch, start string) error {
	_, err := r.run(ctx, "switch", "--no-track", "-c", branch, start)
	return err
}

// CommitAll stages every change outside Exclude and commits it. It returns
// committed=false when there was nothing to commit, and otherwise the new
// commit's full object name. Each trailer, such as "Ralph-Task: T-1", is
// passed to git commit --trailer (git 2.32+). Commit hooks run.
func (r Repo) CommitAll(ctx context.Context, message string, trailers ...string) (hash string, committed bool, err error) {
	if _, err := r.run(ctx, append([]string{"add", "--all"}, r.pathspec()...)...); err != nil {
		return "", false, err
	}
	if _, err := r.run(ctx, "diff", "--cached", "--quiet"); err == nil {
		return "", false, nil
	} else if exitCode(err) != 1 {
		return "", false, err
	}
	args := []string{"commit", "--quiet", "-m", message}
	for _, t := range trailers {
		args = append(args, "--trailer", t)
	}
	if _, err := r.run(ctx, args...); err != nil {
		return "", false, err
	}
	hash, err = r.RevParse(ctx, "HEAD")
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

// ListFiles lists tracked and untracked-but-not-ignored files under pathspec
// (literal, relative to Dir), as Dir-relative slash-separated paths.
func (r Repo) ListFiles(ctx context.Context, pathspec string) ([]string, error) {
	out, err := r.runRaw(ctx, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", ":(literal)"+pathspec)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// PushResult is the outcome of Push.
type PushResult struct {
	// Pushed is false when the remote branch was already up to date.
	Pushed         bool
	Remote, Branch string
}

// Push pushes the local branch to the same-named branch on remote and sets it
// as the upstream, whichever branch is checked out. It never forces: a remote
// that has diverged fails with git's rejection as a *Error.
func (r Repo) Push(ctx context.Context, remote, branch string) (PushResult, error) {
	if remote == "" {
		return PushResult{}, errors.New("git: push needs a remote")
	}
	if branch == "" {
		return PushResult{}, errors.New("git: push needs a branch")
	}
	ref := "refs/heads/" + branch
	args := []string{"push", "--porcelain", "--set-upstream", remote, ref + ":" + ref}
	out, err := r.runRaw(ctx, args...)
	if err != nil {
		return PushResult{}, err
	}
	res := PushResult{Remote: remote, Branch: branch}
	found := false
	for _, line := range strings.Split(out, "\n") {
		// Ref lines are "<flag>\t<from>:<to>\t<summary>"; the flag may be a space.
		parts := strings.Split(line, "\t")
		if len(parts) < 3 || len(parts[0]) != 1 || !strings.Contains(parts[1], ":") {
			continue
		}
		switch parts[0][0] {
		case '=':
			found = true
		case ' ', '*':
			found, res.Pushed = true, true
		default:
			return PushResult{}, &Error{Args: args, Msg: "unexpected push status: " + strings.TrimSpace(line), Err: errors.New("unexpected push status")}
		}
	}
	if !found {
		return PushResult{}, &Error{Args: args, Msg: "unrecognized push output: " + strings.TrimSpace(out), Err: errors.New("unrecognized push output")}
	}
	return res, nil
}
