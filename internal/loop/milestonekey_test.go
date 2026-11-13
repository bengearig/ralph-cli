package loop

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/drift"
	"github.com/bengearig/ralph/internal/harness"
	"github.com/bengearig/ralph/internal/project"
	"github.com/bengearig/ralph/internal/search"
	"github.com/bengearig/ralph/internal/service"
	"github.com/bengearig/ralph/internal/store/sqlite"
	"github.com/bengearig/ralph/internal/taskmanager/fake"
)

const prefixTemplate = "{{.Task.CardKeyPrefix}}{{.Task.ID}}: {{.Task.Title}}"

func TestCommitMessageFallsBackToMilestoneKey(t *testing.T) {
	t.Parallel()
	i, m := Ref{ID: "I-1", Slug: "sso", Title: "SSO"}, Ref{ID: "M-2", Slug: "login", Title: "Login"}
	base := service.TaskView{ID: "T-12", Title: "Title"}
	own := base
	own.Card = &service.CardView{Key: "ABC-1"}
	for _, tc := range []struct {
		name, tmpl, milestoneKey string
		task                     service.TaskView
		want                     string
	}{
		{"own over milestone", prefixTemplate, "ABC-9", own, "[ABC-1] T-12: Title"},
		{"milestone fallback", prefixTemplate, "ABC-9", base, "[ABC-9] T-12: Title"},
		{"own, no milestone card", prefixTemplate, "", own, "[ABC-1] T-12: Title"},
		{"neither", prefixTemplate, "", base, "T-12: Title"},
		{"key field fallback", "{{.Task.CardKey}} {{.Task.ID}}", "ABC-9", base, "ABC-9 T-12"},
		{"template without key", "{{.Task.ID}}: {{.Task.Title}}", "ABC-9", base, "T-12: Title"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ct, err := parseCommitTemplate(tc.tmpl)
			if err != nil {
				t.Fatal(err)
			}
			got, err := commitMessage(ct, commitData{Task: commitTask(tc.task, tc.milestoneKey), Initiative: i, Milestone: m})
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("message = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCommitSubjectsMilestoneKey(t *testing.T) {
	t.Parallel()
	i, m := Ref{ID: "I-1", Slug: "sso", Title: "SSO"}, Ref{ID: "M-2", Slug: "login", Title: "Login"}
	base := service.TaskView{ID: "T-12", Title: "Title"}
	own := base
	own.Card = &service.CardView{Key: "ABC-1"}
	for _, tc := range []struct {
		name, tmpl, milestoneKey string
		task                     service.TaskView
		want                     []string
	}{
		{"no own card", prefixTemplate, "ABC-9", base, []string{"[ABC-9] T-12: Title", "T-12: Title"}},
		{"own card", prefixTemplate, "ABC-9", own, []string{"[ABC-1] T-12: Title", "[ABC-9] T-12: Title", "T-12: Title"}},
		{"own card, no milestone card", prefixTemplate, "", own, []string{"[ABC-1] T-12: Title", "T-12: Title"}},
		{"no cards", prefixTemplate, "", base, []string{"T-12: Title"}},
		{"template without key", "{{.Task.ID}}: {{.Task.Title}}", "ABC-9", own, []string{"T-12: Title"}},
		{"own equals milestone", prefixTemplate, "ABC-1", own, []string{"[ABC-1] T-12: Title", "T-12: Title"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			subjects, err := CommitSubjects(tc.tmpl, tc.milestoneKey, i, m)
			if err != nil {
				t.Fatal(err)
			}
			if got := subjects(tc.task); !slices.Equal(got, tc.want) {
				t.Errorf("subjects = %q, want %q", got, tc.want)
			}
		})
	}
}

func newMilestoneKeyEnv(t *testing.T, taskKey, milestoneKey string) (*runEnv, *fake.Provider) {
	t.Helper()
	e := newRunEnv(t, "a")
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(e.root, project.DirName, "ralph.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	prov := fake.New()
	e.svc = service.New(st, &search.Index{Store: st, Chunker: search.Chunker{MaxChars: 500}}, service.Options{StallThreshold: 2, TaskManager: prov})
	e.cfg.TaskManager.Provider = "fake"
	e.cfg.Loop.CommitTemplate = prefixTemplate
	e.cfg.Loop.CommitCardTrailer = true
	if taskKey != "" {
		if err := e.svc.RecordCard(ctx, domain.KindTask, 1, "fake", taskKey, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if milestoneKey != "" {
		if err := e.svc.RecordCard(ctx, domain.KindMilestone, e.mID, "fake", milestoneKey, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	return e, prov
}

func TestRunCommitMilestoneKeyFallback(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, taskKey, milestoneKey string
		cardTrailer                 bool
		subject, trailer            string
	}{
		{"own card", "ABC-1", "ABC-9", true, "[ABC-1] T-1: a", "Card: ABC-1"},
		{"milestone card", "", "ABC-9", true, "[ABC-9] T-1: a", "Card: ABC-9"},
		{"no cards", "", "", true, "T-1: a", ""},
		{"own card, trailer off", "ABC-1", "ABC-9", false, "[ABC-1] T-1: a", ""},
		{"milestone card, trailer off", "", "ABC-9", false, "[ABC-9] T-1: a", ""},
		{"no cards, trailer off", "", "", false, "T-1: a", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := newMilestoneKeyEnv(t, tc.taskKey, tc.milestoneKey)
			e.cfg.Loop.CommitCardTrailer = tc.cardTrailer
			h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) { return e.complete(id) }}
			if _, err := e.run(t, context.Background(), h); err != nil {
				t.Fatal(err)
			}
			if got := gitCmd(t, e.root, "log", "-1", "--format=%s"); got != tc.subject {
				t.Errorf("subject = %q, want %q", got, tc.subject)
			}
			trailers := gitCmd(t, e.root, "log", "-1", "--format=%(trailers:key=Card,only,unfold)")
			if strings.TrimSpace(trailers) != tc.trailer {
				t.Errorf("Card trailer = %q, want %q", trailers, tc.trailer)
			}
		})
	}
}

func TestRunCommitTemplateWithoutKeyUnchangedByMilestoneCard(t *testing.T) {
	t.Parallel()
	e, _ := newMilestoneKeyEnv(t, "", "ABC-9")
	e.cfg.Loop.CommitTemplate = "{{.Task.ID}}: {{.Task.Title}}"
	h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) { return e.complete(id) }}
	if _, err := e.run(t, context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if got := gitCmd(t, e.root, "log", "-1", "--format=%s"); got != "T-1: a" {
		t.Errorf("subject = %q", got)
	}
}

func TestPlanAndDriftInputNeverCallProvider(t *testing.T) {
	t.Parallel()
	e, prov := newMilestoneKeyEnv(t, "", "ABC-9")
	ctx := context.Background()
	plan, err := Planner{Service: e.svc, Config: e.cfg, Root: e.root}.Plan(ctx, e.mID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.MilestoneCardKey != "ABC-9" {
		t.Errorf("plan milestone key = %q", plan.MilestoneCardKey)
	}
	in, err := DriftInput(ctx, e.svc, e.cfg, e.root, e.mID)
	if err != nil {
		t.Fatal(err)
	}
	if got := in.Subjects(e.status(t, 1)); !slices.Equal(got, []string{"[ABC-9] T-1: a", "T-1: a"}) {
		t.Errorf("subjects = %q", got)
	}
	if calls := prov.Calls(); len(calls) != 0 {
		t.Errorf("provider called: %+v", calls)
	}
}

// Commits whose patch no longer matches and that carry no trailer are found by subject.
func TestDriftMatchesMilestoneKeyAndKeylessSubjects(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, milestoneKeyAtCommit string
	}{
		{"milestone key", "ABC-9"},
		{"no key", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := newMilestoneKeyEnv(t, "", tc.milestoneKeyAtCommit)
			ctx := context.Background()
			e.cfg.Loop.CommitTrailer = false
			e.cfg.Loop.CommitCardTrailer = false
			h := &fakeHarness{script: func(_ int, id int64, _ harness.Request) (harness.Result, error) { return e.complete(id) }}
			if _, err := e.run(t, ctx, h); err != nil {
				t.Fatal(err)
			}
			want := "[ABC-9] T-1: a"
			if tc.milestoneKeyAtCommit == "" {
				want = "T-1: a"
			}
			if got := gitCmd(t, e.root, "log", "-1", "--format=%s"); got != want {
				t.Fatalf("subject = %q, want %q", got, want)
			}
			if err := os.WriteFile(filepath.Join(e.root, "extra.txt"), []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitCmd(t, e.root, "add", "extra.txt")
			gitCmd(t, e.root, "commit", "--quiet", "--amend", "--no-edit")

			if tc.milestoneKeyAtCommit == "" {
				if err := e.svc.RecordCard(ctx, domain.KindMilestone, e.mID, "fake", "ABC-9", "", ""); err != nil {
					t.Fatal(err)
				}
			}
			in, err := DriftInput(ctx, e.svc, e.cfg, e.root, e.mID)
			if err != nil {
				t.Fatal(err)
			}
			r, err := drift.Check(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Tasks) == 0 {
				t.Fatal("no tasks checked")
			}
			for _, tk := range r.Tasks {
				if tk.Match != drift.MatchSubject {
					t.Errorf("task %s match = %q, want %q", tk.ID, tk.Match, drift.MatchSubject)
				}
			}
		})
	}
}
