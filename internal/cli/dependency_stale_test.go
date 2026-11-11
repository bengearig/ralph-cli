package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/loop"
	"github.com/bengearig/ralph/internal/service"
)

func TestStaleBaseDependencyIsReportedByReadOnlyViews(t *testing.T) {
	h, git, commit := newGitTestEnv(t)
	addBareRemote(t, git, "origin")
	h.ok("initiative", "create", "--title", "SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Dep")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "After", "--depends-on", "M-1")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Dep work", "--description", "d", "--criteria", "c")
	h.ok("task", "create", "--milestone", "M-2", "--title", "After work", "--description", "d", "--criteria", "c")
	h.ok("task", "status", "T-1", "TODO")
	h.ok("task", "status", "T-1", "IN_PROGRESS")
	h.ok("task", "status", "T-1", "DONE", "--reason", "done")
	h.ok("task", "status", "T-2", "TODO")

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
	git("merge", "--quiet", "--no-ff", "-m", "Merge dep", "ralph/dep")
	git("push", "--quiet", "origin", "main")
	git("reset", "--quiet", "--hard", base)
	remoteTip := git("rev-parse", "refs/remotes/origin/main")
	fetches := traceFetches(t)

	reason := "is merged into origin/main, but local main is behind"
	fix := "git pull --ff-only origin main"
	line := "depends on: M-1 (waiting: " + reason + ")\n"
	if out := h.ok("milestone", "show", "M-2"); !strings.Contains(out, line) || strings.Contains(out, "ralph milestone merge") {
		t.Fatalf("show: %s", out)
	}
	var shown waitingShow
	h.json(&shown, "milestone", "show", "M-2")
	if len(shown.WaitingOn) != 1 || shown.WaitingOn[0].State != loop.DependencyBaseStale || shown.WaitingOn[0].Reason != reason || shown.WaitingOn[0].Fix != fix {
		t.Fatalf("show json = %+v", shown.WaitingOn)
	}

	if got := git("rev-parse", "refs/remotes/origin/main"); got != remoteTip || fetches() != 0 {
		t.Errorf("show moved the remote-tracking ref or fetched (fetches %d)", fetches())
	}

	out := h.ok("loop", "--initiative", "I-1", "--dry-run")
	want := "M-2 depends on M-1, which " + reason + "; " + fix
	if !strings.Contains(out, "M-2 depends on M-1, which is merged into origin/main") || strings.Contains(out, "M-2 depends on M-1, which branch") {
		t.Errorf("chain overview missing %q:\n%s", want, out)
	}
	var res struct {
		Entries []struct {
			Milestone   string `json:"milestone"`
			Disposition string `json:"disposition"`
			Reason      string `json:"reason"`
			Fix         string `json:"fix"`
		} `json:"entries"`
	}
	h.json(&res, "loop", "--initiative", "I-1", "--dry-run")
	var found bool
	for _, e := range res.Entries {
		if e.Milestone == "M-2" {
			found = true
			if e.Disposition != "waiting" || e.Reason != want || e.Fix != fix {
				t.Errorf("M-2 entry = %+v", e)
			}
		}
	}
	if !found {
		t.Fatalf("no M-2 entry: %+v", res.Entries)
	}

	useChainHarness(t, &chainHarness{h: h})
	_, _, err = h.run("", "loop", "--milestone", "M-2")
	if !errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(err.Error(), "local main is 2 commit(s) behind origin/main") || strings.Contains(err.Error(), "depends on") {
		t.Fatalf("loop err = %v", err)
	}
}
