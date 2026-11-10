package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/loop"
	"github.com/bengearig/ralph/internal/service"
)

// countGit puts a logging git ahead on PATH and returns the number of calls so far.
func countGit(t *testing.T) func() int {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() int {
		b, err := os.ReadFile(log)
		if err != nil {
			return 0
		}
		return strings.Count(string(b), "\n")
	}
}

type waitingShow struct {
	WaitingOn []loop.DependencyFinding `json:"waiting_on"`
	DependsOn []string                 `json:"depends_on"`
}

type waitingStatus struct {
	Initiatives []struct {
		Milestones []struct {
			ID        string   `json:"id"`
			WaitingOn []string `json:"waiting_on"`
		} `json:"milestones"`
	} `json:"initiatives"`
}

func (s waitingStatus) waiting() map[string][]string {
	out := map[string][]string{}
	for _, i := range s.Initiatives {
		for _, m := range i.Milestones {
			if len(m.WaitingOn) > 0 {
				out[m.ID] = m.WaitingOn
			}
		}
	}
	return out
}

func statusLine(out, id string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), id+" ") {
			return l
		}
	}
	return ""
}

func TestShowAndStatusWaitingOn(t *testing.T) {
	h, git, commit := newGitTestEnv(t)
	h.ok("initiative", "create", "--title", "SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Dep")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Two", "--depends-on", "M-1")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Three", "--depends-on", "M-1")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Gone")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "After gone", "--depends-on", "M-4")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Dep work", "--description", "d", "--criteria", "c")
	h.ok("task", "create", "--milestone", "M-4", "--title", "Dropped", "--description", "d", "--criteria", "c")
	h.ok("task", "status", "T-2", "CANCELLED", "--reason", "no")
	h.ok("task", "status", "T-1", "TODO")
	h.ok("task", "status", "T-1", "IN_PROGRESS")

	if out := h.ok("milestone", "show", "M-2"); !strings.Contains(out, "depends on: M-1 (waiting: is IN_PROGRESS, not DONE)\n") {
		t.Fatalf("in progress: %s", out)
	}
	var shown waitingShow
	h.json(&shown, "milestone", "show", "M-2")
	if len(shown.WaitingOn) != 1 || shown.WaitingOn[0].ID != "M-1" || strings.Join(shown.DependsOn, ",") != "M-1" {
		t.Fatalf("json = %+v", shown)
	}
	if out := h.ok("milestone", "show", "M-5"); !strings.Contains(out, "depends on: M-4 (cancelled)\n") {
		t.Fatalf("cancelled: %s", out)
	}
	if out := h.ok("--json", "milestone", "show", "M-5"); strings.Contains(out, "waiting_on") {
		t.Fatalf("cancelled dependency is waiting: %s", out)
	}

	h.ok("task", "status", "T-1", "DONE", "--reason", "done")
	base := git("rev-parse", "HEAD")
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Embeddings.Provider = config.EmbeddingsNone
	svc, closeFn, err := service.Open(context.Background(), cfg, h.dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	git("switch", "--quiet", "-c", "ralph/dep")
	head := commit("dep.txt", "dep work")
	git("switch", "--quiet", "main")
	if err := svc.RecordMilestoneGit(context.Background(), 1, domain.MilestoneGit{Branch: "ralph/dep", BaseCommit: base, HeadCommit: head}); err != nil {
		t.Fatal(err)
	}

	if out := h.ok("milestone", "show", "M-2"); !strings.Contains(out, "depends on: M-1 (waiting: branch ralph/dep is not merged into main)\n") {
		t.Fatalf("unmerged: %s", out)
	}

	calls := countGit(t)
	statusOut := h.ok("status")
	if l := statusLine(statusOut, "M-2"); !strings.Contains(l, "waiting on M-1") {
		t.Fatalf("M-2 line %q in\n%s", l, statusOut)
	}
	for _, id := range []string{"M-1", "M-4", "M-5"} {
		if l := statusLine(statusOut, id); strings.Contains(l, "waiting on") {
			t.Errorf("%s annotated: %q", id, l)
		}
	}
	both := calls()
	var st waitingStatus
	h.json(&st, "status")
	w := st.waiting()
	if len(w) != 2 || strings.Join(w["M-2"], ",") != "M-1" || strings.Join(w["M-3"], ",") != "M-1" {
		t.Fatalf("waiting_on = %v", w)
	}
	h.ok("milestone", "update", "M-3", "--no-depends-on")
	before := calls()
	h.ok("status")
	if one := calls() - before; one != both || both == 0 {
		t.Errorf("git calls: one dependent %d, two dependents %d", one, both)
	}
	h.ok("milestone", "update", "M-3", "--depends-on", "M-1")

	git("merge", "--quiet", "--ff-only", "ralph/dep")
	if out := h.ok("milestone", "show", "M-2"); !strings.Contains(out, "depends on: M-1 (merged)\n") {
		t.Fatalf("merged: %s", out)
	}
	if out := h.ok("--json", "milestone", "show", "M-2"); strings.Contains(out, "waiting_on") {
		t.Fatalf("merged json: %s", out)
	}
	if out := h.ok("status"); strings.Contains(out, "waiting on") {
		t.Fatalf("status after merge:\n%s", out)
	}
	if out := h.ok("--json", "status"); strings.Contains(out, "waiting_on") {
		t.Fatalf("status json after merge: %s", out)
	}
}

func TestWaitingOnSkipsFinishedMilestones(t *testing.T) {
	h, _, _ := newGitTestEnv(t)
	h.ok("initiative", "create", "--title", "SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Dep")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Done one", "--depends-on", "M-1")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Dropped", "--depends-on", "M-1")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Dep work", "--description", "d", "--criteria", "c")
	h.ok("task", "create", "--milestone", "M-2", "--title", "Work", "--description", "d", "--criteria", "c")
	h.ok("task", "create", "--milestone", "M-3", "--title", "Dropped", "--description", "d", "--criteria", "c")
	h.ok("task", "status", "T-1", "TODO")
	h.ok("task", "status", "T-2", "TODO")
	h.ok("task", "status", "T-2", "IN_PROGRESS")
	h.ok("task", "status", "T-2", "DONE", "--reason", "done")
	h.ok("task", "status", "T-3", "CANCELLED", "--reason", "no")
	out := h.ok("status")
	for _, id := range []string{"M-2", "M-3"} {
		if l := statusLine(out, id); strings.Contains(l, "waiting on") {
			t.Errorf("%s annotated: %q", id, l)
		}
	}
	if out := h.ok("milestone", "show", "M-2"); !strings.Contains(out, "depends on: M-1\n") {
		t.Errorf("done milestone show: %s", out)
	}
}

func TestWaitingOnWithoutGit(t *testing.T) {
	h := depsFixture(t)
	h.ok("milestone", "update", "M-2", "--depends-on", "M-1")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Work", "--description", "d", "--criteria", "c")
	h.ok("task", "status", "T-1", "TODO")
	h.ok("task", "status", "T-1", "IN_PROGRESS")

	out, errOut, err := h.run("", "milestone", "show", "M-2")
	if err != nil || !strings.Contains(out, "depends on: M-1\n") || errOut != "" {
		t.Fatalf("show: %q %q %v", out, errOut, err)
	}
	if strings.Contains(out, "waiting") {
		t.Errorf("show annotated: %s", out)
	}
	out, errOut, err = h.run("", "status")
	if err != nil || strings.Contains(out, "waiting on") || errOut != "" {
		t.Fatalf("status: %q %q %v", out, errOut, err)
	}
	if out := h.ok("--json", "status"); strings.Contains(out, "waiting_on") {
		t.Errorf("status json: %s", out)
	}
	if out := h.ok("--json", "milestone", "show", "M-2"); strings.Contains(out, "waiting_on") {
		t.Errorf("show json: %s", out)
	}
}
