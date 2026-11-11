package taskmanager

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
)

type stub struct{}

func (stub) Name() string                                      { return "stub" }
func (stub) Validate() error                                   { return nil }
func (stub) Create(context.Context, CardRequest) (Card, error) { return Card{}, nil }
func (stub) Update(context.Context, string, CardRequest) error { return nil }
func (stub) SetStatus(context.Context, string, string) error   { return nil }

func cfgFor(name string) *config.Config {
	c := &config.Config{}
	c.TaskManager.Provider = name
	return c
}

func unregister(t *testing.T, name string) {
	t.Cleanup(func() {
		mu.Lock()
		delete(factories, name)
		mu.Unlock()
	})
}

func TestHash(t *testing.T) {
	base := CardRequest{Kind: domain.KindTask, ID: "T-1", Title: "a", Description: "bc", ParentKey: "P-1"}
	if base.Hash() != base.Hash() {
		t.Fatal("hash not deterministic")
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(base.Hash()) {
		t.Errorf("hash %q not 64 lowercase hex", base.Hash())
	}
	variants := map[string]CardRequest{
		"kind":        {Kind: domain.KindMilestone, ID: "T-1", Title: "a", Description: "bc", ParentKey: "P-1"},
		"id":          {Kind: domain.KindTask, ID: "T-2", Title: "a", Description: "bc", ParentKey: "P-1"},
		"title":       {Kind: domain.KindTask, ID: "T-1", Title: "x", Description: "bc", ParentKey: "P-1"},
		"description": {Kind: domain.KindTask, ID: "T-1", Title: "a", Description: "x", ParentKey: "P-1"},
		"parent":      {Kind: domain.KindTask, ID: "T-1", Title: "a", Description: "bc", ParentKey: "P-2"},
		"status":      {Kind: domain.KindTask, ID: "T-1", Title: "a", Description: "bc", ParentKey: "P-1", Status: "TODO"},
		"boundary":    {Kind: domain.KindTask, ID: "T-1", Title: "ab", Description: "c", ParentKey: "P-1"},
	}
	for name, v := range variants {
		if v.Hash() == base.Hash() {
			t.Errorf("changing %s did not change the hash", name)
		}
	}
}

func TestNewNone(t *testing.T) {
	p, err := New(cfgFor(config.TaskManagerNone))
	if p != nil || err != nil {
		t.Errorf("New(none) = %v, %v", p, err)
	}
}

func TestNewUnknown(t *testing.T) {
	_, err := New(cfgFor("nope-unknown"))
	if err == nil || !strings.Contains(err.Error(), `"nope-unknown"`) || !strings.Contains(err.Error(), "registered:") {
		t.Errorf("err = %v", err)
	}
}

func TestNewEmptyRegistryMessage(t *testing.T) {
	mu.Lock()
	saved := factories
	factories = map[string]Factory{}
	mu.Unlock()
	t.Cleanup(func() { mu.Lock(); factories = saved; mu.Unlock() })
	_, err := New(cfgFor("x"))
	want := `unknown task manager provider "x" (registered: (none))`
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %s", err, want)
	}
	factories["b"] = func(*config.Config) (Provider, error) { return nil, nil }
	factories["a"] = func(*config.Config) (Provider, error) { return nil, nil }
	_, err = New(cfgFor("x"))
	want = `unknown task manager provider "x" (registered: a, b)`
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %s", err, want)
	}
}

func TestRegisterAndNew(t *testing.T) {
	unregister(t, "test-ok")
	want := stub{}
	Register("test-ok", func(*config.Config) (Provider, error) { return want, nil })
	got, err := New(cfgFor("test-ok"))
	if err != nil || got != Provider(want) {
		t.Errorf("New = %v, %v", got, err)
	}
}

func TestFactoryErrorUnchanged(t *testing.T) {
	unregister(t, "test-err")
	boom := errors.New("boom")
	Register("test-err", func(*config.Config) (Provider, error) { return nil, boom })
	if _, err := New(cfgFor("test-err")); err != boom {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestDuplicateRegisterPanics(t *testing.T) {
	unregister(t, "test-dup")
	f := func(*config.Config) (Provider, error) { return nil, nil }
	Register("test-dup", f)
	defer func() {
		if recover() == nil {
			t.Error("expected panic")
		}
	}()
	Register("test-dup", f)
}

type linkingStub struct{ stub }

func (linkingStub) LinkPullRequest(context.Context, string, PullRequestLink) error { return nil }

func TestPullRequestLinkerIsOptional(t *testing.T) {
	var plain Provider = stub{}
	if _, ok := plain.(PullRequestLinker); ok {
		t.Error("stub without LinkPullRequest satisfies PullRequestLinker")
	}
	var linking Provider = linkingStub{}
	if _, ok := linking.(PullRequestLinker); !ok {
		t.Error("stub with LinkPullRequest does not satisfy PullRequestLinker")
	}
}
