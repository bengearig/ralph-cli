package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/service"
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
