package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/service"
)

// advanceBare adds n commits to main on the bare remote through a clone.
func advanceBare(t *testing.T, bare string, n int) {
	t.Helper()
	clone := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = clone
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("clone", "--quiet", "--branch", "main", bare, ".")
	for i := 0; i < n; i++ {
		name := "remote-" + string(rune('a'+i)) + ".txt"
		if err := os.WriteFile(filepath.Join(clone, name), []byte("r\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", name)
		run("commit", "--quiet", "-m", "remote "+name)
	}
	run("push", "--quiet", "origin", "main")
}

func TestSyncCommand(t *testing.T) {
	h, git, _ := newGitTestEnv(t)
	bare := addBareRemote(t, git, "origin")
	from := git("rev-parse", "main")

	out, errOut, err := h.run("", "sync")
	if err != nil || out != "main is already up to date with origin/main\n" {
		t.Fatalf("current: %q %q %v", out, errOut, err)
	}

	advanceBare(t, bare, 3)
	tipOut, err := exec.Command("git", "-C", bare, "rev-parse", "main").Output()
	if err != nil {
		t.Fatal(err)
	}
	to := strings.TrimSpace(string(tipOut))

	out, _, err = h.run("", "sync", "--dry-run")
	want := "would fast-forward main to origin/main: " + from[:12] + ".." + to[:12] + " (3 commit(s))\n"
	if err != nil || out != want {
		t.Fatalf("dry run: %q %v, want %q", out, err, want)
	}
	if got := git("rev-parse", "main"); got != from {
		t.Errorf("dry run moved main to %s", got)
	}

	out, _, err = h.run("", "sync")
	want = "fast-forwarded main to origin/main: " + from[:12] + ".." + to[:12] + " (3 commit(s))\n"
	if err != nil || out != want {
		t.Fatalf("sync: %q %v, want %q", out, err, want)
	}
	if got := git("rev-parse", "main"); got != to {
		t.Errorf("main = %s, want %s", got, to)
	}
}

func TestSyncCommandAheadAndJSON(t *testing.T) {
	h, git, commit := newGitTestEnv(t)
	addBareRemote(t, git, "origin")
	tip := commit("ahead.txt", "ahead")
	out, errOut, err := h.run("", "sync")
	if err != nil || out != "" || !strings.Contains(errOut, "main is 1 commit(s) ahead of origin/main; nothing to sync") {
		t.Fatalf("ahead: %q %q %v", out, errOut, err)
	}

	out, _, err = h.run("", "--json", "sync")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if len(raw) != 11 {
		t.Errorf("keys = %v", raw)
	}
	for k, want := range map[string]any{"remote": "origin", "base_branch": "main", "state": "ahead", "behind": 0.0, "ahead": 1.0, "from": tip, "to": tip, "dry_run": false, "synced": false} {
		if raw[k] != want {
			t.Errorf("%s = %v, want %v", k, raw[k], want)
		}
	}
	if nm, ok := raw["newly_merged"].([]any); !ok || len(nm) != 0 {
		t.Errorf("newly_merged = %#v, want []", raw["newly_merged"])
	}
	if w, ok := raw["warnings"].([]any); !ok || len(w) != 1 {
		t.Errorf("warnings = %v", raw["warnings"])
	}
}

func TestSyncCommandNeedsHuman(t *testing.T) {
	t.Run("diverged", func(t *testing.T) {
		h, git, commit := newGitTestEnv(t)
		bare := addBareRemote(t, git, "origin")
		advanceBare(t, bare, 1)
		commit("local.txt", "local")
		_, _, err := h.run("", "sync")
		if !errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(err.Error(), "have diverged (1 ahead, 1 behind)") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("fetch failure", func(t *testing.T) {
		h, git, _ := newGitTestEnv(t)
		addBareRemote(t, git, "origin")
		git("remote", "rename", "origin", "elsewhere")
		_, _, err := h.run("", "sync")
		if !errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(err.Error(), "could not fetch") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestSyncCommandHelp(t *testing.T) {
	h, _, _ := newGitTestEnv(t)
	help := h.ok("sync", "--help")
	for _, want := range []string{"--dry-run", "--delete-branches", "Exit codes: 0 when synced or already current, 3 when it needs a person"} {
		if !strings.Contains(help, want) {
			t.Errorf("help lacks %q", want)
		}
	}
	if root := h.ok("--help"); !strings.Contains(root, "Fast-forward the base branch to its remote") {
		t.Errorf("root help lacks sync:\n%s", root)
	}
}

func TestSyncCommandReportsMergedMilestones(t *testing.T) {
	h, git, commit := newGitTestEnv(t)
	bare := addBareRemote(t, git, "origin")
	base := git("rev-parse", "HEAD")

	h.ok("initiative", "create", "--title", "SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Login")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Route", "--description", "d", "--criteria", "c")
	h.ok("task", "status", "T-1", "TODO")
	h.ok("task", "status", "T-1", "IN_PROGRESS")
	h.ok("task", "status", "T-1", "DONE", "--reason", "done")
	git("switch", "--quiet", "-c", "ralph/login")
	head := commit("route.txt", "T-1: work\n\nRalph-Task: T-1")
	git("switch", "--quiet", "main")

	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Embeddings.Provider = config.EmbeddingsNone
	svc, closeFn, err := service.Open(context.Background(), cfg, h.dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = svc.RecordMilestoneGit(context.Background(), 1, domain.MilestoneGit{Branch: "ralph/login", BaseCommit: base, HeadCommit: head})
	closeFn()
	if err != nil {
		t.Fatal(err)
	}

	git("push", "--quiet", "origin", "ralph/login")
	clone := t.TempDir()
	for _, args := range [][]string{
		{"clone", "--quiet", "--branch", "main", bare, "."},
		{"merge", "--quiet", "--no-ff", "-m", "merge login", "origin/ralph/login"},
		{"push", "--quiet", "origin", "main"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = clone
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	out := h.ok("sync", "--dry-run", "--delete-branches")
	if !strings.HasSuffix(out, "\nwould be merged: M-1 (branch ralph/login, kept locally)\n  would delete branch ralph/login\n") {
		t.Errorf("dry run: %q", out)
	}
	if git("branch", "--list", "ralph/login") == "" {
		t.Fatal("dry run deleted the branch")
	}
	out = h.ok("sync", "--dry-run")
	if !strings.HasSuffix(out, "\nwould be merged: M-1 (branch ralph/login, kept locally)\n") {
		t.Errorf("dry run: %q", out)
	}
	out = h.ok("sync", "--delete-branches", "--json")
	if !strings.Contains(out, `"branch_deleted": true`) || strings.Contains(out, "delete_error") {
		t.Errorf("json: %s", out)
	}
	if git("branch", "--list", "ralph/login") != "" {
		t.Error("branch not deleted")
	}
	if out = h.ok("sync"); strings.Contains(out, "merged:") {
		t.Errorf("a second sync reported merges: %q", out)
	}
}
