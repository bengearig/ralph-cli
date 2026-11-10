package jira

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/taskmanager"
	"github.com/bengearig/ralph/internal/taskmanager/jira/adf"
)

const (
	maxSummaryRunes     = 255
	maxDescriptionRunes = 30000
	truncatedNote       = "\n\n_(truncated, see ralph)_"
)

var _ taskmanager.Provider = (*Provider)(nil)

func init() {
	taskmanager.Register(config.TaskManagerJira, func(cfg *config.Config) (taskmanager.Provider, error) {
		return NewProvider(cfg.TaskManager.Jira, cfg.TaskManager.Timeout, os.LookupEnv, nil), nil
	})
}

// Provider maps ralph resources onto Jira cards.
type Provider struct {
	cfg        config.JiraConfig
	timeout    time.Duration
	lookup     func(string) (string, bool)
	httpClient *http.Client

	mu     sync.Mutex
	client *Client
}

// NewProvider never fails and never reads credentials; the client is built on
// first use.
func NewProvider(cfg config.JiraConfig, timeout time.Duration, lookup func(string) (string, bool), httpClient *http.Client) *Provider {
	return &Provider{cfg: cfg, timeout: timeout, lookup: lookup, httpClient: httpClient}
}

func (p *Provider) Name() string { return config.TaskManagerJira }

// Validate reports every configuration problem at once, without network calls.
func (p *Provider) Validate() error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	c := p.cfg
	if u, err := url.Parse(c.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		bad("task_manager.jira.base_url %q is not an http(s) URL", c.BaseURL)
	}
	for _, f := range []struct{ key, val string }{
		{"project_key", c.ProjectKey},
		{"issue_types.initiative", c.IssueTypes.Initiative},
		{"issue_types.milestone", c.IssueTypes.Milestone},
		{"issue_types.task", c.IssueTypes.Task},
	} {
		if strings.TrimSpace(f.val) == "" {
			bad("task_manager.jira.%s must not be empty", f.key)
		}
	}
	for _, f := range []struct{ setting, name string }{
		{"email_env", c.EmailEnv},
		{"token_env", c.TokenEnv},
	} {
		if strings.TrimSpace(f.name) == "" {
			bad("task_manager.jira.%s must not be empty", f.setting)
			continue
		}
		if v, ok := p.lookup(f.name); !ok || strings.TrimSpace(v) == "" {
			bad("environment variable %s (task_manager.jira.%s) is not set", f.name, f.setting)
		}
	}
	return errors.Join(errs...)
}

func (p *Provider) Create(ctx context.Context, req taskmanager.CardRequest) (taskmanager.Card, error) {
	fields, err := p.mapRequest(req)
	if err != nil {
		return taskmanager.Card{}, err
	}
	c, err := p.getClient()
	if err != nil {
		return taskmanager.Card{}, err
	}
	key, err := c.CreateIssue(ctx, fields)
	if err != nil {
		return taskmanager.Card{}, err
	}
	return taskmanager.Card{Key: key, URL: strings.TrimRight(p.cfg.BaseURL, "/") + "/browse/" + key}, nil
}

func (p *Provider) Update(ctx context.Context, key string, req taskmanager.CardRequest) error {
	fields, err := p.mapRequest(req)
	if err != nil {
		return err
	}
	c, err := p.getClient()
	if err != nil {
		return err
	}
	return c.UpdateIssue(ctx, key, fields)
}

// SetStatus moves the card to the Jira status mapped to the ralph status; a
// blank or missing mapping does nothing.
func (p *Provider) SetStatus(ctx context.Context, key, status string) error {
	var target string
	if status == string(domain.StatusCancelled) {
		target = p.cfg.CanceledStatus
	} else {
		target = p.cfg.StatusMap[status]
	}
	if strings.TrimSpace(target) == "" {
		return nil
	}
	c, err := p.getClient()
	if err != nil {
		return err
	}
	return c.TransitionIssue(ctx, key, strings.TrimSpace(target))
}

func (p *Provider) getClient() (*Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil {
		return p.client, nil
	}
	c, err := NewClient(p.cfg, p.timeout, p.lookup, p.httpClient)
	if err != nil {
		return nil, err
	}
	p.client = c
	return c, nil
}

func (p *Provider) mapRequest(req taskmanager.CardRequest) (IssueFields, error) {
	var issueType string
	switch req.Kind {
	case domain.KindInitiative:
		issueType = p.cfg.IssueTypes.Initiative
	case domain.KindMilestone:
		issueType = p.cfg.IssueTypes.Milestone
	case domain.KindTask:
		issueType = p.cfg.IssueTypes.Task
	default:
		return IssueFields{}, fmt.Errorf("jira: %s has unsupported kind %q", req.ID, req.Kind)
	}
	parent := ""
	if req.Kind != domain.KindInitiative {
		if req.ParentKey == "" {
			return IssueFields{}, fmt.Errorf("jira: %s needs its parent card; create the parent first", req.ID)
		}
		parent = req.ParentKey
	}
	return IssueFields{
		Project:     p.cfg.ProjectKey,
		IssueType:   issueType,
		Summary:     summary(req),
		Description: adf.FromMarkdown(body(req)).JSON(),
		Labels:      []string{"ralph", req.ID},
		ParentKey:   parent,
	}, nil
}

func summary(req taskmanager.CardRequest) string {
	return truncateRunes(req.ID+": "+req.Title, maxSummaryRunes, "…")
}

// truncateRunes cuts s to max runes total, ending with suffix when cut.
func truncateRunes(s string, max int, suffix string) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	keep := max - utf8.RuneCountInString(suffix)
	return string(r[:keep]) + suffix
}

func body(req taskmanager.CardRequest) string {
	desc := req.Description
	if utf8.RuneCountInString(desc) > maxDescriptionRunes {
		desc = string([]rune(desc)[:maxDescriptionRunes]) + truncatedNote
	}
	footer := "---\n\nManaged by ralph (" + req.ID + "). The summary and description are overwritten when ralph pushes changes; edit them in ralph."
	if strings.TrimSpace(desc) == "" {
		return footer
	}
	return desc + "\n\n" + footer
}
