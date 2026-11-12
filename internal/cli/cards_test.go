package cli

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/service"
	"github.com/bengearig/ralph/internal/taskmanager"
	tmfake "github.com/bengearig/ralph/internal/taskmanager/fake"
)

func jiraEnv(t *testing.T, token string) *testEnv {
	t.Helper()
	h := newTestEnv(t, true)
	t.Setenv("RALPH_TASK_MANAGER__PROVIDER", "jira")
	t.Setenv("RALPH_TASK_MANAGER__JIRA__BASE_URL", "http://127.0.0.1:1")
	t.Setenv("RALPH_TASK_MANAGER__JIRA__PROJECT_KEY", "RAL")
	t.Setenv("RALPH_JIRA_EMAIL", "me@example.com")
	t.Setenv("RALPH_JIRA_TOKEN", token)
	return h
}

func jiraCards(t *testing.T, h *testEnv) []service.CardView {
	t.Helper()
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Embeddings.Provider = config.EmbeddingsNone
	svc, closeFn, err := service.OpenWithTaskManager(context.Background(), cfg, h.dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	cards, err := svc.ListCards(context.Background(), "jira")
	if err != nil {
		t.Fatal(err)
	}
	return cards
}

func TestMisconfiguredProviderNeverFailsCommand(t *testing.T) {
	h := jiraEnv(t, "")
	out, errOut, err := h.run("", "initiative", "create", "--title", "SSO")
	if err != nil {
		t.Fatalf("create failed: %v\n%s", err, errOut)
	}
	if !strings.Contains(out, "created I-1") {
		t.Errorf("stdout = %q", out)
	}
	if n := strings.Count(errOut, "task manager disabled:"); n != 1 {
		t.Errorf("disabled line count = %d in %q", n, errOut)
	}
	if !strings.Contains(errOut, "RALPH_JIRA_TOKEN") {
		t.Errorf("stderr does not name the variable: %q", errOut)
	}
	if cards := jiraCards(t, h); len(cards) != 0 {
		t.Errorf("cards = %+v", cards)
	}
}

func TestNoProviderPrintsNoDisabledLine(t *testing.T) {
	h := newTestEnv(t, true)
	_, errOut, err := h.run("", "initiative", "create", "--title", "SSO")
	if err != nil || strings.Contains(errOut, "task manager disabled") {
		t.Errorf("err = %v, stderr = %q", err, errOut)
	}
}

func TestDisabledLinePrintedOncePerProcess(t *testing.T) {
	h := jiraEnv(t, "")
	var errOut strings.Builder
	a := &app{streams: Streams{Out: &strings.Builder{}, Err: &errOut}, projectDir: h.dir}
	a.root = a.newRootCommand()
	defer a.close()
	for i := 0; i < 2; i++ {
		if _, err := a.service(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(errOut.String(), "task manager disabled:"); n != 1 {
		t.Errorf("disabled line count = %d in %q", n, errOut.String())
	}
}

func TestCardWarningReachesStderr(t *testing.T) {
	h := jiraEnv(t, "secret")
	h.ok("initiative", "create", "--title", "SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Callback")

	out, errOut, err := h.run("", "task", "create", "--milestone", "M-1", "--title", "Route")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if !strings.Contains(out, "created T-1") || strings.Contains(out, "warning") {
		t.Errorf("stdout = %q", out)
	}
	if !strings.Contains(errOut, "warning: ") || !strings.Contains(errOut, "T-1") || !strings.Contains(errOut, "jira card for") {
		t.Errorf("stderr = %q", errOut)
	}

	out, errOut, err = h.run("", "--json", "task", "create", "--milestone", "M-1", "--title", "Other")
	if err != nil {
		t.Fatalf("json create failed: %v", err)
	}
	var v struct {
		ID          string `json:"id"`
		CardWarning string `json:"card_warning"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%q", err, out)
	}
	if v.CardWarning == "" || !strings.Contains(errOut, "warning: ") {
		t.Errorf("card_warning = %q, stderr = %q", v.CardWarning, errOut)
	}
}

func recordJiraCard(t *testing.T, h *testEnv, kind domain.Kind, id int64, key, url string) {
	t.Helper()
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Embeddings.Provider = config.EmbeddingsNone
	svc, closeFn, err := service.OpenWithTaskManager(context.Background(), cfg, h.dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if err := svc.RecordCard(context.Background(), kind, id, "jira", key, url, "stale"); err != nil {
		t.Fatal(err)
	}
}

func TestShowCommandsPrintCards(t *testing.T) {
	h := jiraEnv(t, "secret")
	h.ok("initiative", "create", "--title", "SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Callback")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Route")

	out := h.ok("task", "show", "T-1")
	if !strings.Contains(out, "card: (pending create)\n") || !strings.Contains(out, "card error: ") {
		t.Errorf("pending create show = %q", out)
	}
	out = h.ok("task", "list")
	if !strings.Contains(out, "CARD") || !strings.Contains(out, "(pending create)") {
		t.Errorf("task list = %q", out)
	}

	recordJiraCard(t, h, domain.KindInitiative, 1, "RAL-1", "https://x.atlassian.net/browse/RAL-1")
	recordJiraCard(t, h, domain.KindMilestone, 1, "RAL-2", "https://x.atlassian.net/browse/RAL-2")
	recordJiraCard(t, h, domain.KindTask, 1, "RAL-12", "https://x.atlassian.net/browse/RAL-12")
	for _, c := range [][]string{{"task", "show", "T-1"}, {"milestone", "show", "M-1"}, {"initiative", "show", "I-1"}} {
		if out := h.ok(c...); !strings.Contains(out, "card: RAL-") || !strings.Contains(out, " https://x.atlassian.net/browse/RAL-") || !strings.Contains(out, "(pending update)") {
			t.Errorf("%v = %q", c, out)
		}
	}
	for _, c := range [][]string{{"task", "list"}, {"milestone", "list"}, {"initiative", "list"}} {
		if out := h.ok(c...); !strings.Contains(out, "CARD") || !strings.Contains(out, "RAL-") {
			t.Errorf("%v = %q", c, out)
		}
	}
	var v struct {
		Card *service.CardView `json:"card"`
	}
	h.json(&v, "--json", "task", "show", "T-1")
	if v.Card == nil || v.Card.Key != "RAL-12" || v.Card.URL == "" || v.Card.Pending != "update" {
		t.Errorf("json card = %+v", v.Card)
	}
}

func TestNoCardColumnWithoutProvider(t *testing.T) {
	h := newTestEnv(t, true)
	t.Setenv("RALPH_TASK_MANAGER__PROVIDER", "none")
	h.ok("initiative", "create", "--title", "SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Callback")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Route")
	for _, c := range [][]string{{"task", "list"}, {"milestone", "list"}, {"initiative", "list"}, {"task", "show", "T-1"}} {
		if out := h.ok(c...); strings.Contains(out, "CARD") || strings.Contains(out, "card") {
			t.Errorf("%v = %q", c, out)
		}
	}
	if out := h.ok("--json", "task", "show", "T-1"); strings.Contains(out, `"card"`) {
		t.Errorf("json = %q", out)
	}
}

func TestConfigValidateReportsDisabledTaskManager(t *testing.T) {
	h := jiraEnv(t, "")
	out, errOut, err := h.run("", "config", "validate")
	if err != nil || !strings.Contains(out, "configuration is valid") {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if !strings.Contains(errOut, "task manager disabled:") || !strings.Contains(errOut, "RALPH_JIRA_TOKEN") {
		t.Errorf("stderr = %q", errOut)
	}
	h = jiraEnv(t, "secret")
	if _, errOut, err := h.run("", "config", "validate"); err != nil || strings.Contains(errOut, "disabled") {
		t.Errorf("err = %v, stderr = %q", err, errOut)
	}
}

func TestTaskStatusCardWarningReachesStderr(t *testing.T) {
	h := jiraEnv(t, "secret")
	h.ok("initiative", "create", "--title", "SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Callback")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Route", "--description", "d", "--criteria", "c")
	recordJiraCard(t, h, domain.KindInitiative, 1, "RAL-1", "https://x.atlassian.net/browse/RAL-1")
	recordJiraCard(t, h, domain.KindMilestone, 1, "RAL-2", "https://x.atlassian.net/browse/RAL-2")
	recordJiraCard(t, h, domain.KindTask, 1, "RAL-12", "https://x.atlassian.net/browse/RAL-12")

	out, errOut, err := h.run("", "task", "status", "T-1", "TODO", "--reason", "ready")
	if err != nil {
		t.Fatalf("status failed: %v\n%s", err, errOut)
	}
	if !strings.Contains(out, "T-1 is now TODO") || strings.Contains(out, "warning") {
		t.Errorf("stdout = %q", out)
	}
	if !strings.Contains(errOut, "warning: ") || !strings.Contains(errOut, "RAL-12") {
		t.Errorf("stderr = %q", errOut)
	}
}

func seedCard(t *testing.T, h *testEnv, kind domain.Kind, id int64, key string) {
	t.Helper()
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Embeddings.Provider = config.EmbeddingsNone
	svc, closeFn, err := service.OpenWithTaskManager(context.Background(), cfg, h.dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if err := svc.RecordCard(context.Background(), kind, id, "jira", key, "http://x/"+key, ""); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteWarnsWhenCardNotMoved(t *testing.T) {
	h := jiraEnv(t, "tok")
	t.Setenv("RALPH_TASK_MANAGER__JIRA__CANCELED_STATUS", "Canceled")
	h.ok("initiative", "create", "--title", "SSO")
	seedCard(t, h, domain.KindInitiative, 1, "RAL-7")

	out, errOut, err := h.run("", "initiative", "delete", "I-1", "--dry-run")
	if err != nil || !strings.Contains(out, `cards: RAL-7 will move to "Canceled"`) {
		t.Fatalf("dry-run: %v\n%s\n%s", err, out, errOut)
	}
	if _, _, err := h.run("", "initiative", "get", "I-1"); err != nil {
		t.Fatalf("dry-run deleted it: %v", err)
	}

	out, errOut, err = h.run("", "initiative", "delete", "I-1", "--yes")
	if err != nil {
		t.Fatalf("delete failed: %v\n%s", err, errOut)
	}
	if !strings.Contains(errOut, "warning: card RAL-7 was not moved:") || !strings.Contains(out, "deleted I-1") {
		t.Errorf("stdout %q stderr %q", out, errOut)
	}
}

func TestDeleteDryRunOmitsCardsWithoutCanceledStatus(t *testing.T) {
	h := jiraEnv(t, "tok")
	h.ok("initiative", "create", "--title", "SSO")
	seedCard(t, h, domain.KindInitiative, 1, "RAL-7")
	out, errOut, err := h.run("", "initiative", "delete", "I-1", "--dry-run")
	if err != nil || strings.Contains(out, "cards:") || !strings.Contains(out, "would delete I-1") {
		t.Fatalf("%v\n%s\n%s", err, out, errOut)
	}
}

var syncFake struct {
	sync.Mutex
	p *tmfake.Provider
}

func init() {
	config.AllowTaskManager("synctest")
	taskmanager.Register("synctest", func(*config.Config) (taskmanager.Provider, error) {
		syncFake.Lock()
		defer syncFake.Unlock()
		return syncFake.p, nil
	})
}

func syncEnv(t *testing.T) (*testEnv, *tmfake.Provider) {
	t.Helper()
	h := newTestEnv(t, true)
	t.Setenv("RALPH_TASK_MANAGER__PROVIDER", "none")
	h.ok("initiative", "create", "--title", "SSO")
	h.ok("milestone", "create", "--initiative", "I-1", "--title", "Callback")
	h.ok("task", "create", "--milestone", "M-1", "--title", "Route")
	p := tmfake.New()
	syncFake.Lock()
	syncFake.p = p
	syncFake.Unlock()
	t.Setenv("RALPH_TASK_MANAGER__PROVIDER", "synctest")
	return h, p
}

func exitIs(err error, target error) bool { return errors.Is(err, target) }

func TestCardsSyncCreatesThenInSync(t *testing.T) {
	h, _ := syncEnv(t)
	out, errOut, err := h.run("", "cards", "sync")
	if err != nil {
		t.Fatalf("sync: %v\n%s", err, errOut)
	}
	for _, id := range []string{"I-1", "M-1", "T-1"} {
		if !regexp.MustCompile(`(?m)^created\s+` + id + `\s+FAKE-\d+`).MatchString(out) {
			t.Errorf("no created line for %s in %q", id, out)
		}
	}
	if !strings.Contains(out, "created 3, updated 0, in sync 0, skipped 0, failed 0") {
		t.Errorf("summary missing: %q", out)
	}
	if n := strings.Count(errOut, "created "); n != 3 {
		t.Errorf("progress lines = %d in %q", n, errOut)
	}

	out, _, err = h.run("", "cards", "sync")
	if err != nil || strings.TrimSpace(out) != "created 0, updated 0, in sync 3, skipped 0, failed 0" {
		t.Errorf("rerun: err = %v, out = %q", err, out)
	}
	out, _, err = h.run("", "cards", "sync", "--verbose")
	if err != nil || strings.Count(out, "in sync ") < 3 || !strings.Contains(out, "T-1") {
		t.Errorf("verbose: err = %v, out = %q", err, out)
	}
}

func TestCardsSyncScope(t *testing.T) {
	h, _ := syncEnv(t)
	t.Setenv("RALPH_TASK_MANAGER__PROVIDER", "none")
	h.ok("initiative", "create", "--title", "Other")
	t.Setenv("RALPH_TASK_MANAGER__PROVIDER", "synctest")
	out := h.ok("cards", "sync", "--initiative", "I-2")
	if !strings.Contains(out, "I-2") || strings.Contains(out, "I-1") || !strings.Contains(out, "created 1,") {
		t.Errorf("out = %q", out)
	}
	h.ok("initiative", "archive", "I-1")
	if out := h.ok("cards", "sync"); !strings.Contains(out, "created 0, updated 0, in sync 1,") {
		t.Errorf("archived not skipped: %q", out)
	}
	if out := h.ok("cards", "sync", "--include-archived"); !strings.Contains(out, "created 3,") {
		t.Errorf("include-archived: %q", out)
	}
	_, _, err := h.run("", "cards", "sync", "--initiative", "I-99")
	if err == nil || errors.Is(err, errs.ErrNeedsHuman) {
		t.Errorf("unknown initiative err = %v", err)
	}
}

func TestCardsSyncFailureExit(t *testing.T) {
	h, p := syncEnv(t)
	p.SetHook(func(_ int, method string, req taskmanager.CardRequest) error {
		if req.ID == "T-1" {
			return errors.New("boom")
		}
		return nil
	})
	out, _, err := h.run("", "cards", "sync")
	if !exitIs(err, errs.ErrNeedsHuman) {
		t.Fatalf("err = %v", err)
	}
	if !regexp.MustCompile(`(?m)^failed\s+T-1\s+.*boom`).MatchString(out) || !strings.Contains(out, "created 2,") || !strings.Contains(out, "failed 1") {
		t.Errorf("out = %q", out)
	}

	h2, p2 := syncEnv(t)
	p2.SetHook(func(_ int, _ string, req taskmanager.CardRequest) error {
		if req.ID == "T-1" {
			return errors.New("boom")
		}
		return nil
	})
	out, errOut, err := h2.run("", "--json", "cards", "sync")
	if !exitIs(err, errs.ErrNeedsHuman) {
		t.Fatalf("json err = %v", err)
	}
	var r service.SyncReport
	if e := json.Unmarshal([]byte(out), &r); e != nil || r.Created != 2 || r.Failed != 1 || len(r.Items) != 3 {
		t.Errorf("json = %q (%v), report = %+v", out, e, r)
	}
	if strings.Contains(errOut, "created I-1") {
		t.Errorf("json mode printed progress: %q", errOut)
	}
}

func TestCardsSyncNotConfigured(t *testing.T) {
	h := newTestEnv(t, true)
	_, _, err := h.run("", "cards", "sync")
	if err == nil || !strings.Contains(err.Error(), "task_manager.provider") || errors.Is(err, errs.ErrNeedsHuman) {
		t.Errorf("none: err = %v", err)
	}

	h = jiraEnv(t, "")
	_, _, err = h.run("", "cards", "sync")
	if err == nil || !strings.Contains(err.Error(), "task manager disabled") || !strings.Contains(err.Error(), "RALPH_JIRA_TOKEN") {
		t.Errorf("jira: err = %v", err)
	}
}

func TestCardsSyncHelp(t *testing.T) {
	h := newTestEnv(t, true)
	out := h.ok("cards", "sync", "--help")
	flat := strings.Join(strings.Fields(out), " ")
	if !strings.Contains(flat, "explicit") || !strings.Contains(flat, "safe to rerun") {
		t.Errorf("help = %q", out)
	}
}

func reconcileCandidates(p *tmfake.Provider) {
	p.SetCandidates([]taskmanager.CardCandidate{
		{Key: "FAKE-90", URL: "https://x/browse/FAKE-90", Summary: "T-99: Gone", Status: "To Do"},
		{Key: "FAKE-91", URL: "https://x/browse/FAKE-91", Summary: "No id here", Status: "To Do"},
	})
}

func TestCardsReconcileDryRun(t *testing.T) {
	h, p := syncEnv(t)
	reconcileCandidates(p)
	out, errOut, err := h.run("", "cards", "reconcile")
	if err != nil {
		t.Fatalf("reconcile: %v\n%s", err, errOut)
	}
	if !regexp.MustCompile(`(?m)^orphan\s+T-99\s+FAKE-90\s+\[To Do\]\s+T-99: Gone\s+https://x/browse/FAKE-90$`).MatchString(out) {
		t.Errorf("no orphan line in %q", out)
	}
	if !regexp.MustCompile(`(?m)^skipped\s+FAKE-91\s+summary has no ralph ID\s+\(No id here\)$`).MatchString(out) {
		t.Errorf("no skipped line in %q", out)
	}
	if !strings.Contains(out, "orphans 1, skipped 1\n") {
		t.Errorf("totals missing: %q", out)
	}
	for _, c := range p.Calls() {
		if c.Method == "SetStatus" {
			t.Errorf("dry run called SetStatus: %+v", c)
		}
	}
}

func TestCardsReconcileJSON(t *testing.T) {
	h, p := syncEnv(t)
	reconcileCandidates(p)
	out := h.ok("--json", "cards", "reconcile")
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("json %q: %v", out, err)
	}
	for _, k := range []string{"items", "orphans", "skipped", "cancelled", "failed"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("missing %q in %q", k, out)
		}
	}
	var r service.ReconcileReport
	if err := json.Unmarshal([]byte(out), &r); err != nil || r.Orphans != 1 || r.Skipped != 1 || len(r.Items) != 2 {
		t.Errorf("report = %+v (%v)", r, err)
	}
}

func TestCardsReconcileCreatedAfter(t *testing.T) {
	h, p := syncEnv(t)
	h.ok("cards", "reconcile", "--created-after", "2026-01-15")
	s := p.Searches()
	want := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	if len(s) != 1 || !s[0].CreatedAfter.Equal(want) {
		t.Fatalf("searches = %+v", s)
	}

	_, _, err := h.run("", "cards", "reconcile", "--created-after", "15/01/2026")
	if err == nil || !errors.Is(err, domain.ErrInvalid) ||
		!strings.Contains(err.Error(), `--created-after: "15/01/2026" is not a date (expected YYYY-MM-DD)`) {
		t.Errorf("err = %v", err)
	}
	if n := len(p.Searches()); n != 1 {
		t.Errorf("bad date searched: %d searches", n)
	}
}

func TestCardsReconcileNotConfigured(t *testing.T) {
	h := newTestEnv(t, true)
	_, _, err := h.run("", "cards", "reconcile")
	if err == nil || !strings.Contains(err.Error(), "task_manager.provider") || errors.Is(err, errs.ErrNeedsHuman) {
		t.Errorf("none: err = %v", err)
	}

	h = jiraEnv(t, "")
	_, _, err = h.run("", "cards", "reconcile")
	if err == nil || !strings.Contains(err.Error(), "task manager disabled") || !strings.Contains(err.Error(), "RALPH_JIRA_TOKEN") {
		t.Errorf("jira: err = %v", err)
	}
}

func TestCardsReconcileHelp(t *testing.T) {
	h := newTestEnv(t, true)
	flat := strings.Join(strings.Fields(h.ok("cards", "reconcile", "--help")), " ")
	for _, want := range []string{"changes nothing", "other people's or other repos' cards", "--created-after"} {
		if !strings.Contains(flat, want) {
			t.Errorf("help lacks %q: %q", want, flat)
		}
	}
	if out := h.ok("cards", "--help"); !strings.Contains(out, "reconcile") {
		t.Errorf("cards help lacks reconcile: %q", out)
	}
}

func reconcileApplyEnv(t *testing.T, canceledStatus string) (*testEnv, *tmfake.Provider) {
	t.Helper()
	h, p := syncEnv(t)
	if canceledStatus != "" {
		t.Setenv("RALPH_TASK_MANAGER__JIRA__CANCELED_STATUS", canceledStatus)
	}
	p.SetCandidates([]taskmanager.CardCandidate{
		{Key: "FAKE-90", URL: "https://x/browse/FAKE-90", Summary: "T-99: Gone", Status: "To Do"},
		{Key: "FAKE-91", URL: "https://x/browse/FAKE-91", Summary: "No id here", Status: "To Do"},
		{Key: "FAKE-92", URL: "https://x/browse/FAKE-92", Summary: "M-98: Also gone", Status: "To Do"},
	})
	return h, p
}

func setStatusKeys(p *tmfake.Provider) []string {
	var keys []string
	for _, c := range p.Calls() {
		if c.Method == "SetStatus" {
			if c.Request.Status != "CANCELLED" {
				keys = append(keys, "bad status "+c.Request.Status)
			}
			keys = append(keys, c.Key)
		}
	}
	return keys
}

func TestCardsReconcileApply(t *testing.T) {
	h, p := reconcileApplyEnv(t, "Canceled")
	out, errOut, err := h.run("", "cards", "reconcile", "--apply", "--created-after", "2026-01-15")
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, errOut)
	}
	if got := strings.Join(setStatusKeys(p), ","); got != "FAKE-90,FAKE-92" {
		t.Errorf("SetStatus calls = %q", got)
	}
	for _, want := range []string{`(?m)^cancelled\s+T-99\s+FAKE-90\b`, `(?m)^cancelled\s+M-98\s+FAKE-92\b`, `(?m)^skipped\s+FAKE-91\b`} {
		if !regexp.MustCompile(want).MatchString(out) {
			t.Errorf("stdout lacks %s: %q", want, out)
		}
	}
	if !strings.Contains(out, "orphans 2, cancelled 2, skipped 1, failed 0\n") {
		t.Errorf("totals missing: %q", out)
	}
	if !strings.Contains(errOut, "cancelled T-99 FAKE-90\n") || !strings.Contains(errOut, "cancelled M-98 FAKE-92\n") {
		t.Errorf("progress missing: %q", errOut)
	}
}

func TestCardsReconcileApplyNeedsScope(t *testing.T) {
	h, p := reconcileApplyEnv(t, "Canceled")
	_, _, err := h.run("", "cards", "reconcile", "--apply")
	if err == nil || !errors.Is(err, domain.ErrInvalid) ||
		!strings.Contains(err.Error(), "--apply needs --created-after YYYY-MM-DD, so that only cards created since this repository started using ralph are considered") {
		t.Fatalf("err = %v", err)
	}
	if len(p.Searches()) != 0 || len(setStatusKeys(p)) != 0 {
		t.Errorf("made calls: searches %v, calls %v", p.Searches(), p.Calls())
	}
}

func TestCardsReconcileApplyNeedsCanceledStatus(t *testing.T) {
	h, p := reconcileApplyEnv(t, "")
	_, _, err := h.run("", "cards", "reconcile", "--apply", "--created-after", "2026-01-15")
	if err == nil || !errors.Is(err, domain.ErrInvalid) ||
		!strings.Contains(err.Error(), "--apply needs task_manager.jira.canceled_status to be set; cards would not be moved") {
		t.Fatalf("err = %v", err)
	}
	if len(p.Searches()) != 0 || len(p.Calls()) != 0 {
		t.Errorf("made calls: searches %v, calls %v", p.Searches(), p.Calls())
	}
}

func TestCardsReconcileApplyFailure(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		h, p := reconcileApplyEnv(t, "Canceled")
		p.SetHook(func(_ int, method string, req taskmanager.CardRequest) error {
			if method == "SetStatus" && req.ID == "FAKE-90" {
				return errors.New("no transition to Canceled")
			}
			return nil
		})
		args := []string{"cards", "reconcile", "--apply", "--created-after", "2026-01-15"}
		if asJSON {
			args = append([]string{"--json"}, args...)
		}
		out, _, err := h.run("", args...)
		if err == nil || !errors.Is(err, errs.ErrNeedsHuman) || !strings.Contains(err.Error(), "1 card(s) could not be cancelled") {
			t.Fatalf("json=%v: err = %v", asJSON, err)
		}
		if got := strings.Join(setStatusKeys(p), ","); got != "FAKE-92" {
			t.Errorf("json=%v: SetStatus calls = %q", asJSON, got)
		}
		if asJSON {
			var r service.ReconcileReport
			if e := json.Unmarshal([]byte(out), &r); e != nil || r.Failed != 1 || r.Cancelled != 1 {
				t.Errorf("report = %+v (%v)", r, e)
			}
		} else if !regexp.MustCompile(`(?m)^failed\s+FAKE-90\s+no transition to Canceled$`).MatchString(out) ||
			!strings.Contains(out, "orphans 2, cancelled 1, skipped 1, failed 1\n") {
			t.Errorf("report = %q", out)
		}
	}
}

func TestCardsReconcileWithoutApplyCancelsNothing(t *testing.T) {
	h, p := reconcileApplyEnv(t, "Canceled")
	h.ok("cards", "reconcile", "--created-after", "2026-01-15")
	if len(setStatusKeys(p)) != 0 {
		t.Errorf("dry run cancelled: %v", p.Calls())
	}
}
