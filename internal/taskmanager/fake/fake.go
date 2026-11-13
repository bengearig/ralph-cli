// Package fake is an in-memory task manager provider for tests.
package fake

import (
	"context"
	"fmt"
	"sync"

	"github.com/bengearig/ralph/internal/taskmanager"
)

// Call is a recorded successful Create, Update, or SetStatus. SetStatus
// records the status in Request.Status.
type Call struct {
	Method  string
	Key     string
	Request taskmanager.CardRequest
}

// LinkCall is a recorded successful LinkPullRequest.
type LinkCall struct {
	Key  string
	Link taskmanager.PullRequestLink
}

// SprintCall is a recorded AddToSprint call.
type SprintCall struct {
	Key     string
	Request taskmanager.SprintRequest
}

// Provider records calls and hands out sequential keys.
type Provider struct {
	mu          sync.Mutex
	calls       []Call
	links       []LinkCall
	sprints     []SprintCall
	sprintNote  string
	sprintErr   error
	candidates  []taskmanager.CardCandidate
	searches    []taskmanager.CardSearch
	n           int
	attempts    int
	err         error
	validateErr error
	preflight   error
	preflights  int
	hook        func(n int, method string, req taskmanager.CardRequest) error
}

var (
	_ taskmanager.Provider    = (*Provider)(nil)
	_ taskmanager.Preflighter = (*Provider)(nil)

	_ taskmanager.PullRequestLinker = (*Provider)(nil)
	_ taskmanager.CardFinder        = (*Provider)(nil)
	_ taskmanager.SprintAdder       = (*Provider)(nil)
)

// New returns an empty fake provider.
func New() *Provider { return &Provider{} }

func (p *Provider) Name() string { return "fake" }

func (p *Provider) Validate() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.validateErr
}

// SetValidateError makes Validate return err.
func (p *Provider) SetValidateError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.validateErr = err
}

// SetPreflightError makes Preflight return err; nil clears it.
func (p *Provider) SetPreflightError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.preflight = err
}

// Preflights returns how many times Preflight was called.
func (p *Provider) Preflights() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.preflights
}

func (p *Provider) Preflight(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.preflights++
	return p.preflight
}

// SetError makes every Create, Update, SetStatus, LinkPullRequest, and
// FindOpenCards return err; nil clears it.
func (p *Provider) SetError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

// SetHook installs a per-call check run before each Create, Update, SetStatus,
// and LinkPullRequest. For SetStatus the request's ID is the card key. n is the 1-based count of those calls so far, including
// this one; a non-nil return fails the call. LinkPullRequest passes an empty
// request.
func (p *Provider) SetHook(h func(n int, method string, req taskmanager.CardRequest) error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hook = h
}

// Calls returns a copy of the successful calls in order.
func (p *Provider) Calls() []Call {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Call(nil), p.calls...)
}

// Links returns a copy of the successful LinkPullRequest calls in order.
func (p *Provider) Links() []LinkCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]LinkCall(nil), p.links...)
}

func (p *Provider) Create(_ context.Context, req taskmanager.CardRequest) (taskmanager.Card, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.check("Create", req); err != nil {
		return taskmanager.Card{}, err
	}
	p.n++
	key := fmt.Sprintf("FAKE-%d", p.n)
	p.calls = append(p.calls, Call{Method: "Create", Key: key, Request: req})
	return taskmanager.Card{Key: key, URL: "https://fake.example/browse/" + key}, nil
}

func (p *Provider) Update(_ context.Context, key string, req taskmanager.CardRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.check("Update", req); err != nil {
		return err
	}
	p.calls = append(p.calls, Call{Method: "Update", Key: key, Request: req})
	return nil
}

func (p *Provider) SetStatus(_ context.Context, key, status string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.check("SetStatus", taskmanager.CardRequest{ID: key, Status: status}); err != nil {
		return err
	}
	p.calls = append(p.calls, Call{Method: "SetStatus", Key: key, Request: taskmanager.CardRequest{Status: status}})
	return nil
}

func (p *Provider) LinkPullRequest(_ context.Context, key string, l taskmanager.PullRequestLink) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.check("LinkPullRequest", taskmanager.CardRequest{}); err != nil {
		return err
	}
	p.links = append(p.links, LinkCall{Key: key, Link: l})
	return nil
}

// SetSprintResult sets what AddToSprint returns.
func (p *Provider) SetSprintResult(note string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sprintNote, p.sprintErr = note, err
}

// SprintCalls returns a copy of the AddToSprint calls in order.
func (p *Provider) SprintCalls() []SprintCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]SprintCall(nil), p.sprints...)
}

// AddToSprint records the call and returns the configured result. It bypasses
// the call hook and SetError, and does not count toward the hook's n.
func (p *Provider) AddToSprint(_ context.Context, key string, r taskmanager.SprintRequest) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sprints = append(p.sprints, SprintCall{Key: key, Request: r})
	return p.sprintNote, p.sprintErr
}

// SetCandidates sets the cards FindOpenCards returns.
func (p *Provider) SetCandidates(c []taskmanager.CardCandidate) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.candidates = append([]taskmanager.CardCandidate(nil), c...)
}

// Searches returns a copy of the FindOpenCards searches in order.
func (p *Provider) Searches() []taskmanager.CardSearch {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]taskmanager.CardSearch(nil), p.searches...)
}

// FindOpenCards records the search and returns the configured candidates. It
// bypasses the call hook and does not count toward the hook's n.
func (p *Provider) FindOpenCards(_ context.Context, s taskmanager.CardSearch) ([]taskmanager.CardCandidate, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.searches = append(p.searches, s)
	if p.err != nil {
		return nil, p.err
	}
	return append([]taskmanager.CardCandidate(nil), p.candidates...), nil
}

func (p *Provider) check(method string, req taskmanager.CardRequest) error {
	p.attempts++
	if p.err != nil {
		return p.err
	}
	if p.hook != nil {
		return p.hook(p.attempts, method, req)
	}
	return nil
}
