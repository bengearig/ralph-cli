package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/pr"
	tmfake "github.com/bengearig/ralph/internal/taskmanager/fake"
)

type linkingProvider struct{ *fakeProvider }

func (linkingProvider) Name() string { return "linkfake" }

func init() {
	pr.RegisterLink("linkfake", func(namespace, repository string, number int) string {
		return fmt.Sprintf("https://example.com/%s/%s/pulls/%d", namespace, repository, number)
	})
}

func (f *prCreateFixture) withCard(t *testing.T, key string) *tmfake.Provider {
	t.Helper()
	newPRProvider = func(*config.Config) (pr.Provider, error) { return linkingProvider{f.fake}, nil }
	p := tmfake.New()
	syncFake.Lock()
	syncFake.p = p
	syncFake.Unlock()
	t.Setenv("RALPH_TASK_MANAGER__PROVIDER", "synctest")
	if key != "" {
		if err := f.svc.RecordCard(t.Context(), domain.KindMilestone, 1, p.Name(), key, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestPRCreateLinksOnCard(t *testing.T) {
	f := newPRCreateFixture(t)
	p := f.withCard(t, "AIN-7")
	out, errOut, err := f.h.run("", "pr", "create", "--milestone", "M-1")
	if err != nil || out != "opened https://example.com/acme/sso/pulls/1\n" || errOut != "" {
		t.Fatalf("opened: %q %q %v", out, errOut, err)
	}
	links := p.Links()
	if len(links) != 1 || links[0].Key != "AIN-7" || links[0].Link.Title != "Pull request sso#1" {
		t.Fatalf("links = %+v", links)
	}
	if out, errOut, err = f.h.run("", "pr", "create", "--milestone", "M-1"); err != nil || errOut != "" || !strings.HasPrefix(out, "exists ") {
		t.Fatalf("exists: %q %q %v", out, errOut, err)
	}
	if len(p.Links()) != 2 {
		t.Errorf("exists links = %d", len(p.Links()))
	}
}

func TestPRCreateLinksOnAdopt(t *testing.T) {
	f := newPRCreateFixture(t)
	f.fake.found = pr.PullRequest{Namespace: "acme", Repository: "web", Number: 7, URL: "https://example.com/pr/7"}
	f.fake.foundOK = true
	p := f.withCard(t, "AIN-7")
	out, errOut, err := f.h.run("", "pr", "create", "--milestone", "M-1")
	if err != nil || !strings.HasPrefix(out, "adopted ") || errOut != "" || len(p.Links()) != 1 || p.Links()[0].Key != "AIN-7" {
		t.Fatalf("adopted: %q %q %v %+v", out, errOut, err, p.Links())
	}
}

func TestPRCreateLinkFailureIsWarning(t *testing.T) {
	f := newPRCreateFixture(t)
	p := f.withCard(t, "AIN-7")
	p.SetError(errors.New("forbidden"))
	out, errOut, err := f.h.run("", "pr", "create", "--milestone", "M-1")
	if err != nil || out != "opened https://example.com/acme/sso/pulls/1\n" {
		t.Fatalf("create: %q %v", out, err)
	}
	if !strings.Contains(errOut, "fake card AIN-7: pull request link not added: forbidden") {
		t.Errorf("stderr = %q", errOut)
	}
	if f.recorded(t).Number != 1 {
		t.Errorf("recorded = %+v", f.recorded(t))
	}

	jout, jerr, err := f.h.run("", "--json", "pr", "create", "--milestone", "M-1")
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Outcome     string `json:"outcome"`
		CardWarning string `json:"card_warning"`
	}
	if err := json.Unmarshal([]byte(jout), &res); err != nil {
		t.Fatalf("stdout %q: %v", jout, err)
	}
	if res.Outcome != "exists" || !strings.Contains(res.CardWarning, "pull request link not added") || !strings.Contains(jerr, "pull request link not added") {
		t.Errorf("json = %+v, stderr %q", res, jerr)
	}

	p.SetError(nil)
	out, errOut, err = f.h.run("", "pr", "create", "--milestone", "M-1")
	if err != nil || !strings.HasPrefix(out, "exists ") || errOut != "" || len(p.Links()) != 1 {
		t.Errorf("retry: %q %q %v links %d", out, errOut, err, len(p.Links()))
	}
	var ok map[string]any
	f.h.json(&ok, "pr", "create", "--milestone", "M-1")
	if _, has := ok["card_warning"]; has {
		t.Errorf("card_warning present on success: %v", ok)
	}
}

func TestPRCreateNoLinkWithoutCardOrTaskManager(t *testing.T) {
	f := newPRCreateFixture(t)
	p := f.withCard(t, "")
	out, errOut, err := f.h.run("", "pr", "create", "--milestone", "M-1")
	if err != nil || errOut != "" || !strings.HasPrefix(out, "opened ") || len(p.Links()) != 0 {
		t.Errorf("no card: %q %q %v %d", out, errOut, err, len(p.Links()))
	}

	t.Setenv("RALPH_TASK_MANAGER__PROVIDER", "none")
	f = newPRCreateFixture(t)
	if out, errOut, err = f.h.run("", "pr", "create", "--milestone", "M-1"); err != nil || errOut != "" || !strings.HasPrefix(out, "opened ") {
		t.Errorf("no task manager: %q %q %v", out, errOut, err)
	}
}

func TestPRCreateDryRunAndFailedPushDoNotLink(t *testing.T) {
	f := newPRCreateFixture(t)
	p := f.withCard(t, "AIN-7")
	f.h.ok("pr", "create", "--milestone", "M-1", "--dry-run")
	if len(p.Links()) != 0 {
		t.Errorf("dry run linked: %+v", p.Links())
	}
	f.git("remote", "set-url", "origin", "/nonexistent/repo.git")
	if _, _, err := f.h.run("", "pr", "create", "--milestone", "M-1"); err == nil {
		t.Fatal("push should fail")
	}
	if len(p.Links()) != 0 {
		t.Errorf("failed push linked: %+v", p.Links())
	}
}
