package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// newRepo creates a repository with one commit on main and an isolated
// identity and config.
func newRepo(t *testing.T) Repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	isolate(t)
	dir := t.TempDir()
	r := Repo{Dir: dir, Exclude: ".ralph"}
	mustRun(t, r, "init", "--quiet", "--initial-branch=main")
	write(t, dir, "README.md", "hello\n")
	mustRun(t, r, "add", "README.md")
	mustRun(t, r, "commit", "--quiet", "-m", "initial")
	return r
}

// isolate points git at a throwaway global config and a fixed identity.
func isolate(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Ralph Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "ralph@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Ralph Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "ralph@example.com")
}

func mustRun(t *testing.T, r Repo, args ...string) string {
	t.Helper()
	out, err := r.run(context.Background(), args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBranches(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	if b, err := r.CurrentBranch(ctx); err != nil || b != "main" {
		t.Fatalf("CurrentBranch = %q, %v", b, err)
	}
	if ok, _ := r.BranchExists(ctx, "main"); !ok {
		t.Error("main should exist")
	}
	if ok, _ := r.BranchExists(ctx, "ralph/x"); ok {
		t.Error("ralph/x should not exist")
	}
	if err := r.CreateBranch(ctx, "ralph/x", "main"); err != nil {
		t.Fatal(err)
	}
	if b, _ := r.CurrentBranch(ctx); b != "ralph/x" {
		t.Errorf("after create = %q", b)
	}
	if err := r.Switch(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	if err := r.Switch(ctx, "nope"); err == nil || !strings.Contains(err.Error(), "git switch nope") {
		t.Errorf("switch to missing branch: %v", err)
	}
	mustRun(t, r, "switch", "--quiet", "--detach")
	if b, err := r.CurrentBranch(ctx); err != nil || b != "" {
		t.Errorf("detached = %q, %v", b, err)
	}
}

func TestCreateBranchFromRemoteDoesNotTrack(t *testing.T) {
	ctx := context.Background()
	origin := newRepo(t)
	clone := Repo{Dir: t.TempDir(), Exclude: ".ralph"}
	mustRun(t, Repo{Dir: filepath.Dir(clone.Dir)}, "clone", "--quiet", origin.Dir, clone.Dir)
	mustRun(t, clone, "switch", "--quiet", "--detach")
	mustRun(t, clone, "branch", "--quiet", "-D", "main")

	if ok, _ := clone.RefExists(ctx, "refs/heads/main"); ok {
		t.Fatal("local main should be gone")
	}
	if ok, _ := clone.RefExists(ctx, "origin/main"); !ok {
		t.Fatal("origin/main should exist")
	}
	if err := clone.CreateBranch(ctx, "ralph/y", "origin/main"); err != nil {
		t.Fatal(err)
	}
	if _, err := clone.run(ctx, "rev-parse", "--abbrev-ref", "ralph/y@{upstream}"); err == nil {
		t.Error("new branch must not track origin/main")
	}
}

func TestDirtyIgnoresExclude(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	write(t, r.Dir, ".ralph/runs/log.jsonl", "{}")
	if d, err := r.Dirty(ctx); err != nil || len(d) != 0 {
		t.Fatalf("Dirty with only .ralph changes = %v, %v", d, err)
	}
	write(t, r.Dir, "README.md", "changed\n")
	write(t, r.Dir, "src/new file.go", "package src\n")
	write(t, r.Dir, "old.txt", "o\n")
	mustRun(t, r, "add", "old.txt")
	mustRun(t, r, "commit", "--quiet", "-m", "old")
	mustRun(t, r, "mv", "old.txt", "renamed.txt")
	d, err := r.Dirty(ctx)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(d)
	if strings.Join(d, ",") != "README.md,renamed.txt,src/new file.go" {
		t.Errorf("Dirty = %v", d)
	}
}

func TestCommitAll(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	if _, committed, err := r.CommitAll(ctx, "nothing"); err != nil || committed {
		t.Fatalf("empty commit = %v, %v", committed, err)
	}
	write(t, r.Dir, ".ralph/runs/log.jsonl", "{}")
	if _, committed, err := r.CommitAll(ctx, "only ralph files"); err != nil || committed {
		t.Fatalf(".ralph-only commit = %v, %v", committed, err)
	}
	write(t, r.Dir, "a.txt", "a\n")
	write(t, r.Dir, "README.md", "edited\n")
	hash, committed, err := r.CommitAll(ctx, "T-1: add a")
	if err != nil || !committed || hash == "" {
		t.Fatalf("commit = %q, %v, %v", hash, committed, err)
	}
	if msg := mustRun(t, r, "log", "-1", "--format=%s"); msg != "T-1: add a" {
		t.Errorf("message = %q", msg)
	}
	files := mustRun(t, r, "show", "--name-only", "--format=", "HEAD")
	if strings.Contains(files, ".ralph") || !strings.Contains(files, "a.txt") || !strings.Contains(files, "README.md") {
		t.Errorf("committed files = %q", files)
	}
	if d, _ := r.Dirty(ctx); len(d) != 0 {
		t.Errorf("still dirty: %v", d)
	}
}

func TestCommitHookFailureIsReported(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	write(t, r.Dir, ".git/hooks/pre-commit", "#!/bin/sh\necho 'lint failed' >&2\nexit 1\n")
	if err := os.Chmod(filepath.Join(r.Dir, ".git/hooks/pre-commit"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, r.Dir, "b.txt", "b\n")
	_, committed, err := r.CommitAll(ctx, "T-2")
	if err == nil || committed || !strings.Contains(err.Error(), "lint failed") {
		t.Errorf("hook failure = %v, %v", committed, err)
	}
}

func TestNotARepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	if _, err := (Repo{Dir: dir}).CurrentBranch(context.Background()); err == nil {
		t.Error("expected error outside a repository")
	}
}

func TestStash(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	if saved, err := r.Stash(ctx, "nothing"); err != nil || saved {
		t.Fatalf("empty stash = %v, %v", saved, err)
	}
	write(t, r.Dir, ".ralph/runs/log.jsonl", "{}")
	write(t, r.Dir, "README.md", "edited\n")
	write(t, r.Dir, "new.txt", "new\n")
	saved, err := r.Stash(ctx, "ralph: T-2 BLOCKED, unfinished work")
	if err != nil || !saved {
		t.Fatalf("stash = %v, %v", saved, err)
	}
	if d, _ := r.Dirty(ctx); len(d) != 0 {
		t.Errorf("still dirty after stash: %v", d)
	}
	if _, err := os.Stat(filepath.Join(r.Dir, ".ralph/runs/log.jsonl")); err != nil {
		t.Error(".ralph files must not be stashed")
	}
	if list := mustRun(t, r, "stash", "list"); !strings.Contains(list, "ralph: T-2 BLOCKED, unfinished work") {
		t.Errorf("stash list = %q", list)
	}
	if files := mustRun(t, r, "stash", "show", "--include-untracked", "--name-only"); !strings.Contains(files, "new.txt") || !strings.Contains(files, "README.md") {
		t.Errorf("stashed files = %q", files)
	}
}
