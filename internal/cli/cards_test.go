package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/config"
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
