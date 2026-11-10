package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/looplock"
)

// newMergeTestEnv leaves a DONE, drift-clean milestone M-1 on ralph/login,
// ahead of main, with main checked out.
func newMergeTestEnv(t *testing.T) (h *testEnv, git func(args ...string) string, commit func(file, msg string) string) {
	t.Helper()
	h, git, commit = newGitTestEnv(t)
	h.ok("initiative", "create", "--title", "SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Login")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Route", "--description", "d", "--criteria", "c")
	git("switch", "--quiet", "-c", "ralph/login")
	h.ok("task", "status", "T-1", "TODO")
	h.ok("task", "status", "T-1", "IN_PROGRESS")
	h.ok("task", "status", "T-1", "DONE", "--reason", "done")
	commit("T-1.txt", "T-1: work\n\nRalph-Task: T-1")
	h.ok("milestone", "reconcile", "M-1", "--accept")
	git("switch", "--quiet", "main")
	return h, git, commit
}

func TestMilestoneMerge(t *testing.T) {
	h, git, _ := newMergeTestEnv(t)
	from := git("rev-parse", "main")
	to := git("rev-parse", "ralph/login")

	out, errOut, err := h.run("", "milestone", "merge", "M-1")
	if err != nil {
		t.Fatalf("merge: %v\n%s", err, errOut)
	}
	want := "merged M-1 into main: " + from[:12] + ".." + to[:12] + "\nbranch ralph/login kept\n"
	if out != want {
		t.Errorf("output = %q, want %q", out, want)
	}
	if got := git("branch", "--show-current"); got != "main" {
		t.Errorf("on %q, want main", got)
	}
	if got := git("rev-parse", "main"); got != to {
		t.Errorf("main = %s, want %s", got, to)
	}

	out = h.ok("milestone", "merge", "M-1", "--delete-branch")
	if out != "M-1 is already merged into main\n" {
		t.Errorf("second run = %q", out)
	}
	if git("branch", "--list", "ralph/login") == "" {
		t.Error("an already merged milestone's branch should be kept")
	}
}

func TestMilestoneMergeDeleteBranch(t *testing.T) {
	h, git, _ := newMergeTestEnv(t)
	out := h.ok("milestone", "merge", "M-1", "--delete-branch")
	if !strings.Contains(out, "\ndeleted branch ralph/login\n") {
		t.Errorf("output = %q", out)
	}
	if git("branch", "--list", "ralph/login") != "" {
		t.Error("branch should be gone")
	}
}

func TestMilestoneMergeDryRun(t *testing.T) {
	h, git, _ := newMergeTestEnv(t)
	from := git("rev-parse", "main")
	to := git("rev-parse", "ralph/login")
	state := func() string {
		return git("rev-parse", "main") + git("branch", "--show-current") + git("branch", "--list") + git("status", "--porcelain")
	}
	before := state()

	out := h.ok("milestone", "merge", "M-1", "--dry-run")
	for _, want := range []string{"would merge M-1 into main: " + from[:12] + ".." + to[:12], "would keep branch ralph/login", "dry run: nothing changed"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	out = h.ok("milestone", "merge", "M-1", "--dry-run", "--delete-branch")
	if !strings.Contains(out, "would delete branch ralph/login") {
		t.Errorf("output = %q", out)
	}
	if after := state(); after != before {
		t.Errorf("dry run changed the repository:\n%s\nto\n%s", before, after)
	}
}

func TestMilestoneMergeRefusals(t *testing.T) {
	needsHuman := func(t *testing.T, h *testEnv, contains ...string) {
		t.Helper()
		for _, args := range [][]string{{"milestone", "merge", "M-1"}, {"--json", "milestone", "merge", "M-1"}} {
			out, _, err := h.run("", args...)
			if !errors.Is(err, errs.ErrNeedsHuman) {
				t.Fatalf("%v: err = %v, want ErrNeedsHuman", args, err)
			}
			for _, c := range contains {
				if !strings.Contains(err.Error(), c) {
					t.Errorf("err = %q, want it to contain %q", err, c)
				}
			}
			if out != "" {
				t.Errorf("a refusal printed %q", out)
			}
		}
	}

	t.Run("not done", func(t *testing.T) {
		h, _, _ := newMergeTestEnv(t)
		h.ok("task", "create", "--milestone", "M-1", "--title", "More", "--description", "d", "--criteria", "c")
		h.ok("task", "status", "T-2", "TODO")
		needsHuman(t, h, "not DONE")
	})
	t.Run("drift", func(t *testing.T) {
		h, git, commit := newMergeTestEnv(t)
		git("switch", "--quiet", "ralph/login")
		commit("extra.txt", "extra")
		git("switch", "--quiet", "main")
		needsHuman(t, h, "ralph milestone reconcile M-1")
	})
	t.Run("dirty tree", func(t *testing.T) {
		h, _, _ := newMergeTestEnv(t)
		if err := os.WriteFile(filepath.Join(h.dir, "scratch.txt"), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		needsHuman(t, h, "scratch.txt")
	})
	t.Run("not a fast-forward", func(t *testing.T) {
		h, _, commit := newMergeTestEnv(t)
		commit("mainline.txt", "mainline")
		needsHuman(t, h, "rebase")
	})
	t.Run("running loop", func(t *testing.T) {
		h, _, _ := newMergeTestEnv(t)
		lock, err := looplock.Acquire(filepath.Join(h.dir, ".ralph", "loop.lock"), "milestone M-9")
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Release()
		_, _, err = h.run("", "milestone", "merge", "M-1")
		if err == nil || errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(err.Error(), "loop") {
			t.Errorf("err = %v, want a plain error naming the loop", err)
		}
	})
	t.Run("missing milestone", func(t *testing.T) {
		h, _, _ := newMergeTestEnv(t)
		_, _, err := h.run("", "milestone", "merge", "M-9")
		if err == nil || errors.Is(err, errs.ErrNeedsHuman) {
			t.Errorf("err = %v, want a plain error", err)
		}
	})
}

func TestMilestoneMergeJSON(t *testing.T) {
	h, git, _ := newMergeTestEnv(t)
	from := git("rev-parse", "main")
	to := git("rev-parse", "ralph/login")
	out, errOut, err := h.run("", "--json", "milestone", "merge", "M-1")
	if err != nil {
		t.Fatalf("merge: %v\n%s", err, errOut)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("stdout is not a single JSON object: %v\n%s", err, out)
	}
	for _, k := range []string{"merged", "already_merged", "branch_deleted", "from", "to", "warnings"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("JSON lacks %q: %s", k, out)
		}
	}
	if raw["merged"] != true || raw["from"] != from || raw["to"] != to {
		t.Errorf("JSON = %s", out)
	}
	if errOut != "" {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestMilestoneMergeHelp(t *testing.T) {
	h := newTestEnv(t, true)
	out := strings.Join(strings.Fields(h.ok("milestone", "merge", "--help")), " ")
	for _, want := range []string{"--dry-run", "--delete-branch", "never creates a merge commit", "never pushes", "left checked out"} {
		if !strings.Contains(out, want) {
			t.Errorf("help lacks %q:\n%s", want, out)
		}
	}
}
