package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
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
	if full := mustRun(t, r, "rev-parse", "HEAD"); hash != full || len(hash) < 40 {
		t.Errorf("hash = %q, want the full object name %q", hash, full)
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

func TestCommitAllTrailers(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)

	write(t, r.Dir, "a.txt", "a\n")
	if _, committed, err := r.CommitAll(ctx, "T-1: add a", "Ralph-Task: T-1"); err != nil || !committed {
		t.Fatalf("commit = %v, %v", committed, err)
	}
	if got := mustRun(t, r, "log", "-1", "--format=%(trailers:key=Ralph-Task,valueonly)"); got != "T-1" {
		t.Errorf("Ralph-Task trailer = %q, want T-1", got)
	}
	if got := mustRun(t, r, "log", "-1", "--format=%s"); got != "T-1: add a" {
		t.Errorf("subject = %q", got)
	}

	write(t, r.Dir, "b.txt", "b\n")
	msg := "T-2: add b\n\nCo-authored-by: X <x@example.com>"
	if _, committed, err := r.CommitAll(ctx, msg, "Ralph-Task: T-2"); err != nil || !committed {
		t.Fatalf("commit = %v, %v", committed, err)
	}
	body := mustRun(t, r, "log", "-1", "--format=%B")
	want := "T-2: add b\n\nCo-authored-by: X <x@example.com>\nRalph-Task: T-2"
	if body != want {
		t.Errorf("message = %q, want one trailer block %q", body, want)
	}
	cmd := exec.Command("git", "interpret-trailers", "--parse")
	cmd.Dir = r.Dir
	cmd.Stdin = strings.NewReader(body + "\n")
	parsed, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(parsed)); got != "Co-authored-by: X <x@example.com>\nRalph-Task: T-2" {
		t.Errorf("interpret-trailers --parse = %q", got)
	}

	write(t, r.Dir, "c.txt", "c\n")
	plain := "T-3: add c\n\nA body paragraph."
	if _, committed, err := r.CommitAll(ctx, plain); err != nil || !committed {
		t.Fatalf("commit = %v, %v", committed, err)
	}
	if got := mustRun(t, r, "log", "-1", "--format=%B"); got != plain {
		t.Errorf("message without trailers = %q, want %q", got, plain)
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

func TestRevParse(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	hash, err := r.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(hash) {
		t.Errorf("RevParse(HEAD) = %q, want a full SHA-1", hash)
	}
	if want := mustRun(t, r, "rev-parse", "main"); hash != want {
		t.Errorf("RevParse(HEAD) = %q, want %q", hash, want)
	}
	mustRun(t, r, "tag", "-a", "-m", "annotated", "v1")
	if tagged, err := r.RevParse(ctx, "v1"); err != nil || tagged != hash {
		t.Errorf("RevParse(v1) = %q, %v; want the tagged commit %q", tagged, err, hash)
	}
	if _, err := r.RevParse(ctx, "nope"); err == nil || !strings.Contains(err.Error(), "git rev-parse") {
		t.Errorf("RevParse(nope) = %v", err)
	}
}

func TestMergeBase(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	fork := mustRun(t, r, "rev-parse", "HEAD")
	mustRun(t, r, "switch", "--quiet", "-c", "feature")
	write(t, r.Dir, "f.txt", "f\n")
	mustRun(t, r, "add", "f.txt")
	mustRun(t, r, "commit", "--quiet", "-m", "feature")
	mustRun(t, r, "switch", "--quiet", "main")
	write(t, r.Dir, "m.txt", "m\n")
	mustRun(t, r, "add", "m.txt")
	mustRun(t, r, "commit", "--quiet", "-m", "main moves on")

	if hash, found, err := r.MergeBase(ctx, "feature", "main"); err != nil || !found || hash != fork {
		t.Errorf("MergeBase(feature, main) = %q, %v, %v; want %q", hash, found, err, fork)
	}

	mustRun(t, r, "switch", "--quiet", "--orphan", "lonely")
	write(t, r.Dir, "o.txt", "o\n")
	mustRun(t, r, "add", "o.txt")
	mustRun(t, r, "commit", "--quiet", "-m", "unrelated")
	if hash, found, err := r.MergeBase(ctx, "lonely", "main"); err != nil || found || hash != "" {
		t.Errorf("MergeBase(lonely, main) = %q, %v, %v; want no common ancestor", hash, found, err)
	}

	if _, _, err := r.MergeBase(ctx, "nope", "main"); err == nil {
		t.Error("MergeBase with a missing ref should fail")
	}
}

// commitFile writes name with body and commits it with message.
func commitFile(t *testing.T, r Repo, name, body, message string) string {
	t.Helper()
	write(t, r.Dir, name, body)
	mustRun(t, r, "add", name)
	mustRun(t, r, "commit", "--quiet", "-m", message)
	return mustRun(t, r, "rev-parse", "HEAD")
}

func TestIsAncestor(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	parent := mustRun(t, r, "rev-parse", "HEAD")
	child := commitFile(t, r, "a.txt", "a\n", "child")
	mustRun(t, r, "switch", "--quiet", "-c", "sibling", parent)
	sibling := commitFile(t, r, "b.txt", "b\n", "sibling")

	for _, tc := range []struct {
		anc, desc string
		want      bool
	}{
		{parent, child, true},
		{child, child, true},
		{child, parent, false},
		{child, sibling, false},
		{sibling, child, false},
	} {
		if got, err := r.IsAncestor(ctx, tc.anc, tc.desc); err != nil || got != tc.want {
			t.Errorf("IsAncestor(%.7s, %.7s) = %v, %v; want %v", tc.anc, tc.desc, got, err, tc.want)
		}
	}
	var gerr *Error
	if _, err := r.IsAncestor(ctx, "nope", child); !errors.As(err, &gerr) {
		t.Errorf("IsAncestor(nope) = %v, want a *git.Error", err)
	}
}

func TestCommitExists(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	head := mustRun(t, r, "rev-parse", "HEAD")
	if ok, err := r.CommitExists(ctx, head); err != nil || !ok {
		t.Errorf("CommitExists(HEAD) = %v, %v", ok, err)
	}
	absent := strings.Repeat("0123456789", 4)
	if ok, err := r.CommitExists(ctx, absent); err != nil || ok {
		t.Errorf("CommitExists(absent) = %v, %v", ok, err)
	}
	blob := mustRun(t, r, "rev-parse", "HEAD:README.md")
	if ok, err := r.CommitExists(ctx, blob); err != nil || ok {
		t.Errorf("CommitExists(blob) = %v, %v", ok, err)
	}
}

func TestLog(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	mustRun(t, r, "switch", "--quiet", "-c", "feature")
	odd := "T-1: pipes | and\ttabs, ünïcødé ✓"
	first := commitFile(t, r, "a.txt", "a\n", odd+"\n\nRalph-Task: T-1")
	second := commitFile(t, r, "b.txt", "b\n", "squashed\n\nbody\n\nRalph-Task: T-2\nOther: x\nRalph-Task: T-3")
	third := commitFile(t, r, "c.txt", "c\n", "plain")

	got, err := r.Log(ctx, "main..feature", "Ralph-Task", "Missing")
	if err != nil {
		t.Fatal(err)
	}
	want := []LogCommit{
		{Hash: third, Subject: "plain", Trailers: map[string][]string{}},
		{Hash: second, Subject: "squashed", Trailers: map[string][]string{"Ralph-Task": {"T-2", "T-3"}}},
		{Hash: first, Subject: odd, Trailers: map[string][]string{"Ralph-Task": {"T-1"}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Log(main..feature) =\n%q\nwant\n%q", got, want)
	}
	for _, c := range got {
		if len(c.Hash) != 40 {
			t.Errorf("hash %q is not a full object name", c.Hash)
		}
	}

	if got, err := r.Log(ctx, "main..feature"); err != nil || len(got) != 3 || got[0].Subject != "plain" {
		t.Errorf("Log without trailer keys = %q, %v", got, err)
	}

	empty, err := r.Log(ctx, "feature..main", "Ralph-Task")
	if err != nil || empty == nil || len(empty) != 0 {
		t.Errorf("Log(empty range) = %#v, %v; want an empty, non-nil slice", empty, err)
	}

	if _, err := r.Log(ctx, "main..nope"); err == nil {
		t.Error("Log with an unknown ref should fail")
	}
	if _, err := r.Log(ctx, "main..feature", "bad,key"); err == nil {
		t.Error("Log with an invalid trailer key should fail")
	}
}

func TestPatchID(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	write(t, r.Dir, "other.txt", "1\n2\n3\n")
	mustRun(t, r, "add", "other.txt")
	mustRun(t, r, "commit", "--quiet", "-m", "other")
	mustRun(t, r, "switch", "--quiet", "-c", "feature")
	orig := commitFile(t, r, "a.txt", "a\n", "add a")
	different := commitFile(t, r, "b.txt", "b\n", "add b")

	mustRun(t, r, "switch", "--quiet", "main")
	commitFile(t, r, "other.txt", "1\n2\n3\n4\n", "main moves on")
	mustRun(t, r, "cherry-pick", orig)
	picked := mustRun(t, r, "rev-parse", "HEAD")
	if picked == orig || mustRun(t, r, "rev-parse", orig+"^") == mustRun(t, r, "rev-parse", picked+"^") {
		t.Fatal("cherry-pick should produce a new commit on a different parent")
	}

	id, err := r.PatchID(ctx, orig)
	if err != nil || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(id) {
		t.Fatalf("PatchID(orig) = %q, %v", id, err)
	}
	if p, err := r.PatchID(ctx, picked); err != nil || p != id {
		t.Errorf("PatchID(cherry-pick) = %q, %v; want %q", p, err, id)
	}
	if d, err := r.PatchID(ctx, different); err != nil || d == "" || d == id {
		t.Errorf("PatchID(different) = %q, %v; want a different patch-id from %q", d, err, id)
	}

	mustRun(t, r, "commit", "--quiet", "--allow-empty", "-m", "empty")
	if e, err := r.PatchID(ctx, "HEAD"); err != nil || e != "" {
		t.Errorf("PatchID(empty commit) = %q, %v; want \"\"", e, err)
	}
	mustRun(t, r, "merge", "--quiet", "--no-ff", "--no-edit", "feature")
	if m, err := r.PatchID(ctx, "HEAD"); err != nil || m != "" {
		t.Errorf("PatchID(merge) = %q, %v; want \"\"", m, err)
	}
	if _, err := r.PatchID(ctx, "nope"); err == nil {
		t.Error("PatchID of an unknown ref should fail")
	}
}

func addCommit(t *testing.T, r Repo, name string) {
	t.Helper()
	write(t, r.Dir, name, name+"\n")
	mustRun(t, r, "add", name)
	mustRun(t, r, "commit", "--quiet", "-m", "add "+name)
}

func TestHeadState(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	head := mustRun(t, r, "rev-parse", "HEAD")
	if b, c, err := r.HeadState(ctx); err != nil || b != "main" || c != head {
		t.Fatalf("HeadState = %q, %q, %v", b, c, err)
	}
	mustRun(t, r, "switch", "--quiet", "--detach")
	if b, c, err := r.HeadState(ctx); err != nil || b != "" || c != head {
		t.Fatalf("detached HeadState = %q, %q, %v", b, c, err)
	}
	mustRun(t, r, "switch", "--quiet", "main")
	addCommit(t, r, "next.txt")
	if err := r.SwitchDetached(ctx, head); err != nil {
		t.Fatal(err)
	}
	if b, c, _ := r.HeadState(ctx); b != "" || c != head {
		t.Errorf("after SwitchDetached = %q, %q", b, c)
	}
}

func TestMergeFFOnly(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	if err := r.CreateBranch(ctx, "feature", "main"); err != nil {
		t.Fatal(err)
	}
	addCommit(t, r, "a.txt")
	tip := mustRun(t, r, "rev-parse", "feature")
	if err := r.Switch(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	if err := r.MergeFFOnly(ctx, "feature"); err != nil {
		t.Fatal(err)
	}
	if got := mustRun(t, r, "rev-parse", "main"); got != tip {
		t.Errorf("main = %s, want %s", got, tip)
	}
	if merges := mustRun(t, r, "rev-list", "--merges", "main"); merges != "" {
		t.Errorf("merge commits: %s", merges)
	}
}

func TestMergeFFOnlyDiverged(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	if err := r.CreateBranch(ctx, "feature", "main"); err != nil {
		t.Fatal(err)
	}
	addCommit(t, r, "a.txt")
	if err := r.Switch(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	addCommit(t, r, "b.txt")
	before := mustRun(t, r, "rev-parse", "HEAD")
	err := r.MergeFFOnly(ctx, "feature")
	if err == nil || !strings.Contains(err.Error(), "fast-forward") {
		t.Fatalf("err = %v, want a fast-forward refusal", err)
	}
	if after := mustRun(t, r, "rev-parse", "HEAD"); after != before {
		t.Errorf("HEAD moved from %s to %s", before, after)
	}
	if st := mustRun(t, r, "status", "--porcelain"); st != "" {
		t.Errorf("working tree changed: %s", st)
	}
}

func TestDeleteBranch(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	if err := r.CreateBranch(ctx, "merged", "main"); err != nil {
		t.Fatal(err)
	}
	if err := r.CreateBranch(ctx, "unmerged", "main"); err != nil {
		t.Fatal(err)
	}
	addCommit(t, r, "a.txt")
	if err := r.Switch(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteBranch(ctx, "merged"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.BranchExists(ctx, "merged"); ok {
		t.Error("merged branch should be gone")
	}
	err := r.DeleteBranch(ctx, "unmerged")
	if err == nil || !strings.Contains(err.Error(), "not fully merged") {
		t.Fatalf("err = %v, want a not fully merged refusal", err)
	}
	if ok, _ := r.BranchExists(ctx, "unmerged"); !ok {
		t.Error("unmerged branch should remain")
	}
}

func TestResetHard(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	head := mustRun(t, r, "rev-parse", "HEAD")
	write(t, r.Dir, "README.md", "changed\n")
	write(t, r.Dir, "staged.txt", "x\n")
	mustRun(t, r, "add", "staged.txt")
	if err := r.ResetHard(ctx, head); err != nil {
		t.Fatal(err)
	}
	if st := mustRun(t, r, "status", "--porcelain"); st != "" {
		t.Errorf("working tree not clean: %s", st)
	}
}

// newRemote creates a bare repository and registers it as origin of r.
func newRemote(t *testing.T, r Repo) Repo {
	t.Helper()
	bare := Repo{Dir: t.TempDir()}
	mustRun(t, bare, "init", "--quiet", "--bare", "--initial-branch=main")
	mustRun(t, r, "remote", "add", "origin", bare.Dir)
	return bare
}

func TestPushNewBranchSetsUpstream(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	bare := newRemote(t, r)
	if err := r.CreateBranch(ctx, "ralph/x", "main"); err != nil {
		t.Fatal(err)
	}
	addCommit(t, r, "x.txt")
	res, err := r.Push(ctx, "origin", "ralph/x")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pushed || res.Remote != "origin" || res.Branch != "ralph/x" {
		t.Errorf("result = %+v", res)
	}
	if got, want := mustRun(t, bare, "rev-parse", "refs/heads/ralph/x"), mustRun(t, r, "rev-parse", "HEAD"); got != want {
		t.Errorf("remote tip = %s, want %s", got, want)
	}
	if got := mustRun(t, r, "config", "branch.ralph/x.remote"); got != "origin" {
		t.Errorf("branch remote = %q", got)
	}
	if got := mustRun(t, r, "config", "branch.ralph/x.merge"); got != "refs/heads/ralph/x" {
		t.Errorf("branch merge = %q", got)
	}
}

func TestPushAgain(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	bare := newRemote(t, r)
	if err := r.CreateBranch(ctx, "ralph/x", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Push(ctx, "origin", "ralph/x"); err != nil {
		t.Fatal(err)
	}
	res, err := r.Push(ctx, "origin", "ralph/x")
	if err != nil || res.Pushed {
		t.Fatalf("second push = %+v, %v; want Pushed false", res, err)
	}
	addCommit(t, r, "more.txt")
	res, err = r.Push(ctx, "origin", "ralph/x")
	if err != nil || !res.Pushed {
		t.Fatalf("push after commit = %+v, %v; want Pushed true", res, err)
	}
	if got, want := mustRun(t, bare, "rev-parse", "refs/heads/ralph/x"), mustRun(t, r, "rev-parse", "HEAD"); got != want {
		t.Errorf("remote tip = %s, want %s", got, want)
	}
}

func TestPushWhileAnotherBranchIsCheckedOut(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	bare := newRemote(t, r)
	if err := r.CreateBranch(ctx, "ralph/x", "main"); err != nil {
		t.Fatal(err)
	}
	addCommit(t, r, "x.txt")
	if err := r.Switch(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	res, err := r.Push(ctx, "origin", "ralph/x")
	if err != nil || !res.Pushed {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if got, want := mustRun(t, bare, "rev-parse", "refs/heads/ralph/x"), mustRun(t, r, "rev-parse", "ralph/x"); got != want {
		t.Errorf("remote tip = %s, want %s", got, want)
	}
	if ok, _ := bare.RefExists(ctx, "refs/heads/main"); ok {
		t.Error("main should not have been pushed")
	}
}

func TestPushDivergedIsRejectedNotForced(t *testing.T) {
	ctx := context.Background()
	r := newRepo(t)
	bare := newRemote(t, r)
	if err := r.CreateBranch(ctx, "ralph/x", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Push(ctx, "origin", "ralph/x"); err != nil {
		t.Fatal(err)
	}
	other := Repo{Dir: t.TempDir()}
	mustRun(t, other, "clone", "--quiet", bare.Dir, other.Dir+"/w")
	clone := Repo{Dir: other.Dir + "/w"}
	mustRun(t, clone, "switch", "--quiet", "ralph/x")
	commitFile(t, clone, "remote.txt", "remote\n", "remote commit")
	mustRun(t, clone, "push", "--quiet", "origin", "HEAD:refs/heads/ralph/x")
	remoteTip := mustRun(t, bare, "rev-parse", "refs/heads/ralph/x")

	addCommit(t, r, "local.txt")
	res, err := r.Push(ctx, "origin", "ralph/x")
	var ge *Error
	if !errors.As(err, &ge) {
		t.Fatalf("err = %v, want *Error", err)
	}
	if res.Pushed {
		t.Error("Pushed should be false on failure")
	}
	if !strings.Contains(err.Error(), "rejected") {
		t.Errorf("error should carry git's rejection: %v", err)
	}
	if got := mustRun(t, bare, "rev-parse", "refs/heads/ralph/x"); got != remoteTip {
		t.Errorf("remote tip changed to %s from %s", got, remoteTip)
	}
}

func TestPushMissingRemote(t *testing.T) {
	r := newRepo(t)
	_, err := r.Push(context.Background(), "nowhere", "main")
	var ge *Error
	if !errors.As(err, &ge) || !strings.Contains(err.Error(), "nowhere") {
		t.Fatalf("err = %v, want *Error mentioning the remote", err)
	}
}

func TestPushRejectsEmptyArguments(t *testing.T) {
	// A directory that is not a repository would make any git run fail with *Error.
	r := Repo{Dir: t.TempDir()}
	for _, tc := range []struct{ remote, branch string }{{"", "main"}, {"origin", ""}} {
		_, err := r.Push(context.Background(), tc.remote, tc.branch)
		var ge *Error
		if err == nil || errors.As(err, &ge) {
			t.Errorf("Push(%q, %q) = %v, want a plain error", tc.remote, tc.branch, err)
		}
	}
}

// pushedRepo returns a repository with main pushed to a new origin.
func pushedRepo(t *testing.T) (Repo, Repo) {
	t.Helper()
	r := newRepo(t)
	bare := newRemote(t, r)
	mustRun(t, r, "push", "--quiet", "origin", "main")
	return r, bare
}

// advanceRemote adds n commits to branch on the bare remote through a scratch clone.
func advanceRemote(t *testing.T, bare Repo, branch string, n int) string {
	t.Helper()
	clone := Repo{Dir: t.TempDir()}
	mustRun(t, clone, "clone", "--quiet", bare.Dir, ".")
	if branch != "main" {
		if _, err := clone.run(context.Background(), "rev-parse", "--verify", "--quiet", "origin/"+branch); err == nil {
			mustRun(t, clone, "checkout", "--quiet", "-B", branch, "origin/"+branch)
		} else {
			mustRun(t, clone, "checkout", "--quiet", "-B", branch)
		}
	}
	var tip string
	for i := 0; i < n; i++ {
		tip = commitFile(t, clone, fmt.Sprintf("remote-%d.txt", i), filepath.Base(clone.Dir)+"\n", "remote commit")
	}
	mustRun(t, clone, "push", "--quiet", "origin", branch)
	return tip
}

func TestFetchBranchUpdatesOnlyRemoteTrackingRef(t *testing.T) {
	ctx := context.Background()
	r, bare := pushedRepo(t)
	if _, err := r.run(ctx, "fetch", "--quiet", "origin"); err != nil {
		t.Fatal(err)
	}
	mustRun(t, r, "switch", "--quiet", "-c", "feature")
	commitFile(t, r, "f.txt", "f\n", "feature work")
	write(t, r.Dir, "README.md", "dirty\n")
	write(t, r.Dir, "untracked.txt", "u\n")
	mustRun(t, r, "add", "untracked.txt")
	mainBefore := mustRun(t, r, "rev-parse", "main")
	headBefore := mustRun(t, r, "rev-parse", "HEAD")
	statusBefore := mustRun(t, r, "status", "--porcelain")
	tip := advanceRemote(t, bare, "main", 2)
	if err := r.FetchBranch(ctx, "origin", "main"); err != nil {
		t.Fatal(err)
	}
	if got := mustRun(t, r, "rev-parse", "refs/remotes/origin/main"); got != tip {
		t.Errorf("origin/main = %s, want %s", got, tip)
	}
	if got := mustRun(t, r, "rev-parse", "main"); got != mainBefore {
		t.Errorf("local main moved to %s", got)
	}
	if got := mustRun(t, r, "rev-parse", "HEAD"); got != headBefore {
		t.Errorf("HEAD moved to %s", got)
	}
	if got := mustRun(t, r, "symbolic-ref", "--short", "HEAD"); got != "feature" {
		t.Errorf("checked-out branch = %s", got)
	}
	if got := mustRun(t, r, "status", "--porcelain"); got != statusBefore {
		t.Errorf("status changed: %q, was %q", got, statusBefore)
	}
	if b, _ := os.ReadFile(filepath.Join(r.Dir, "README.md")); string(b) != "dirty\n" {
		t.Errorf("work tree file changed: %q", b)
	}
}

func TestFetchBranchLeavesOtherBranchesAndTags(t *testing.T) {
	ctx := context.Background()
	r, bare := pushedRepo(t)
	advanceRemote(t, bare, "other", 1)
	if err := r.FetchBranch(ctx, "origin", "main"); err != nil {
		t.Fatal(err)
	}
	otherTip := advanceRemote(t, bare, "other", 1)
	mustRun(t, bare, "tag", "v1", "main")
	tip := advanceRemote(t, bare, "main", 1)
	if err := r.FetchBranch(ctx, "origin", "main"); err != nil {
		t.Fatal(err)
	}
	if got := mustRun(t, r, "rev-parse", "refs/remotes/origin/main"); got != tip {
		t.Errorf("origin/main = %s, want %s", got, tip)
	}
	if ok, _ := r.RefExists(ctx, "refs/remotes/origin/other"); ok {
		t.Error("origin/other should not have been fetched")
	}
	if err := r.FetchBranch(ctx, "origin", "other"); err != nil {
		t.Fatal(err)
	}
	if got := mustRun(t, r, "rev-parse", "refs/remotes/origin/other"); got != otherTip {
		t.Errorf("origin/other = %s, want %s", got, otherTip)
	}
	if out := mustRun(t, r, "tag", "--list"); out != "" {
		t.Errorf("tags fetched: %q", out)
	}
}

func TestFetchBranchWithoutConfiguredRefspec(t *testing.T) {
	r, bare := pushedRepo(t)
	mustRun(t, r, "config", "--unset-all", "remote.origin.fetch")
	tip := advanceRemote(t, bare, "main", 1)
	if err := r.FetchBranch(context.Background(), "origin", "main"); err != nil {
		t.Fatal(err)
	}
	if got := mustRun(t, r, "rev-parse", "refs/remotes/origin/main"); got != tip {
		t.Errorf("origin/main = %s, want %s", got, tip)
	}
}

func TestFetchBranchAfterForceMove(t *testing.T) {
	ctx := context.Background()
	r, bare := pushedRepo(t)
	advanceRemote(t, bare, "main", 2)
	if err := r.FetchBranch(ctx, "origin", "main"); err != nil {
		t.Fatal(err)
	}
	first := mustRun(t, bare, "rev-parse", "main~2")
	mustRun(t, bare, "update-ref", "refs/heads/main", first)
	other := Repo{Dir: t.TempDir()}
	mustRun(t, other, "clone", "--quiet", bare.Dir, ".")
	tip := commitFile(t, other, "rewrite.txt", "x\n", "rewritten")
	mustRun(t, other, "push", "--quiet", "origin", "main")
	if err := r.FetchBranch(ctx, "origin", "main"); err != nil {
		t.Fatal(err)
	}
	if got := mustRun(t, r, "rev-parse", "refs/remotes/origin/main"); got != tip {
		t.Errorf("origin/main = %s, want %s", got, tip)
	}
	mustRun(t, bare, "update-ref", "refs/heads/main", first)
	if err := r.FetchBranch(ctx, "origin", "main"); err != nil {
		t.Fatalf("non-fast-forward fetch: %v", err)
	}
	if got := mustRun(t, r, "rev-parse", "refs/remotes/origin/main"); got != first {
		t.Errorf("origin/main = %s, want %s", got, first)
	}
}

func TestFetchBranchFailures(t *testing.T) {
	r, _ := pushedRepo(t)
	notRepo := t.TempDir()
	mustRun(t, r, "remote", "add", "broken", notRepo)
	for name, tc := range map[string]struct{ remote, branch string }{
		"unknown remote": {"nowhere", "main"},
		"not a repo":     {"broken", "main"},
		"missing branch": {"origin", "absent"},
	} {
		err := r.FetchBranch(context.Background(), tc.remote, tc.branch)
		var ge *Error
		if !errors.As(err, &ge) {
			t.Errorf("%s: err = %v, want *Error", name, err)
		}
	}
}

func TestFetchBranchRejectsEmptyArguments(t *testing.T) {
	r := Repo{Dir: t.TempDir()}
	for _, tc := range []struct{ remote, branch string }{{"", "main"}, {"origin", ""}} {
		err := r.FetchBranch(context.Background(), tc.remote, tc.branch)
		var ge *Error
		if err == nil || errors.As(err, &ge) {
			t.Errorf("FetchBranch(%q, %q) = %v, want a plain error", tc.remote, tc.branch, err)
		}
	}
}

func TestFetchBranchCancelledContext(t *testing.T) {
	r, _ := pushedRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := r.FetchBranch(ctx, "origin", "main"); err == nil {
		t.Error("want an error for a cancelled context")
	}
	if time.Since(start) > 5*time.Second {
		t.Error("cancelled fetch did not return promptly")
	}
}

func TestFetchBranchDisablesTerminalPromptOnlyForFetch(t *testing.T) {
	r, _ := pushedRepo(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shimDir := t.TempDir()
	log := filepath.Join(shimDir, "env.log")
	script := "#!/bin/sh\nprintf '%s %s\\n' \"$1\" \"${GIT_TERMINAL_PROMPT-unset}\" >> " + log + "\nexec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_TERMINAL_PROMPT", "")
	os.Unsetenv("GIT_TERMINAL_PROMPT")
	ctx := context.Background()
	if err := r.FetchBranch(ctx, "origin", "main"); err != nil {
		t.Fatal(err)
	}
	mustRun(t, r, "status", "--porcelain")
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if !slices.Contains(lines, "fetch 0") {
		t.Errorf("fetch did not get GIT_TERMINAL_PROMPT=0: %q", lines)
	}
	if !slices.Contains(lines, "status unset") {
		t.Errorf("status env was altered: %q", lines)
	}
}

func TestCompareBase(t *testing.T) {
	ctx := context.Background()
	r, bare := pushedRepo(t)
	fetch := func() {
		t.Helper()
		if err := r.FetchBranch(ctx, "origin", "main"); err != nil {
			t.Fatal(err)
		}
	}
	check := func(want BaseState, behind, ahead int) BaseStatus {
		t.Helper()
		st, err := r.CompareBase(ctx, "origin", "main")
		if err != nil {
			t.Fatal(err)
		}
		if st.State != want || st.Behind != behind || st.Ahead != ahead {
			t.Errorf("status = %+v, want %s behind %d ahead %d", st, want, behind, ahead)
		}
		if wl := mustRun(t, r, "rev-parse", "refs/heads/main"); st.Local != wl {
			t.Errorf("Local = %s, want %s", st.Local, wl)
		}
		if wr := mustRun(t, r, "rev-parse", "refs/remotes/origin/main"); st.Remote != wr {
			t.Errorf("Remote = %s, want %s", st.Remote, wr)
		}
		return st
	}
	fetch()
	if st := check(BaseCurrent, 0, 0); len(st.Local) != 40 {
		t.Errorf("Local is not a full hash: %q", st.Local)
	}
	advanceRemote(t, bare, "main", 3)
	fetch()
	check(BaseBehind, 3, 0)
	mustRun(t, r, "merge", "--quiet", "--ff-only", "origin/main")
	commitFile(t, r, "local1.txt", "l\n", "local 1")
	commitFile(t, r, "local2.txt", "l\n", "local 2")
	check(BaseAhead, 0, 2)
	advanceRemote(t, bare, "main", 1)
	fetch()
	check(BaseDiverged, 1, 2)
}

func TestCompareBaseMissingRefs(t *testing.T) {
	ctx := context.Background()
	r, _ := pushedRepo(t)
	if _, err := r.CompareBase(ctx, "origin", "absent"); !errors.Is(err, ErrNoLocalBase) {
		t.Errorf("missing local: err = %v, want ErrNoLocalBase", err)
	}
	mustRun(t, r, "branch", "local-only")
	_, err := r.CompareBase(ctx, "origin", "local-only")
	if err == nil || errors.Is(err, ErrNoLocalBase) || !strings.Contains(err.Error(), "never fetched") {
		t.Errorf("missing remote-tracking ref: err = %v", err)
	}
}
