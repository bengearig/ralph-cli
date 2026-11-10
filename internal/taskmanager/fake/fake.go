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

// Provider records calls and hands out sequential keys.
type Provider struct {
	mu          sync.Mutex
	calls       []Call
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

// SetError makes every Create, Update, and SetStatus return err; nil clears it.
func (p *Provider) SetError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

// SetHook installs a per-call check run before each Create, Update, and
// SetStatus. n is the 1-based count of those calls so far, including this one; a
// non-nil return fails the call.
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
	if err := p.check("SetStatus", taskmanager.CardRequest{Status: status}); err != nil {
		return err
	}
	p.calls = append(p.calls, Call{Method: "SetStatus", Key: key, Request: taskmanager.CardRequest{Status: status}})
	return nil
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
