package jira

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
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

var (
	_ taskmanager.Provider    = (*Provider)(nil)
	_ taskmanager.Preflighter = (*Provider)(nil)

	_ taskmanager.PullRequestLinker = (*Provider)(nil)
	_ taskmanager.CardFinder        = (*Provider)(nil)
)

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

	mu          sync.Mutex
	client      *Client
	currentUser *User
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
	for _, l := range c.Labels {
		if strings.TrimSpace(l) == "" || strings.ContainsAny(l, " \t\r\n") {
			bad("task_manager.jira.labels %q must not be empty or contain whitespace", l)
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

// Preflight checks that the configured issue types exist in the project and, when Jira
// reports hierarchy levels, that each parent type sits exactly one level above its child.
func (p *Provider) Preflight(ctx context.Context) error {
	c, err := p.getClient()
	if err != nil {
		return err
	}
	types, err := c.ProjectIssueTypes(ctx, p.cfg.ProjectKey)
	if err != nil {
		return err
	}
	byName := map[string]IssueType{}
	names := make([]string, 0, len(types))
	for _, t := range types {
		byName[strings.ToLower(t.Name)] = t
		names = append(names, t.Name)
	}
	slices.Sort(names)

	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	configured := []struct{ setting, name string }{
		{"initiative", p.cfg.IssueTypes.Initiative},
		{"milestone", p.cfg.IssueTypes.Milestone},
		{"task", p.cfg.IssueTypes.Task},
	}
	found := make([]IssueType, len(configured))
	known := true
	for i, c := range configured {
		t, ok := byName[strings.ToLower(strings.TrimSpace(c.name))]
		if !ok {
			bad("task_manager.jira.issue_types.%s %q is not an issue type of project %s (it has: %s)", c.setting, c.name, p.cfg.ProjectKey, strings.Join(names, ", "))
			known = false
			continue
		}
		found[i] = t
		if t.Level == nil {
			known = false
		}
	}
	if known {
		for i := 0; i < len(configured)-1; i++ {
			parent, child := found[i], found[i+1]
			if *parent.Level == *child.Level+1 {
				continue
			}
			want := *child.Level + 1
			var fits []string
			for _, t := range types {
				if t.Level != nil && *t.Level == want {
					fits = append(fits, t.Name)
				}
			}
			slices.Sort(fits)
			hint := "the project has no type at that level"
			if len(fits) > 0 {
				hint = "the project has: " + strings.Join(fits, ", ")
			}
			bad("%s type %q is level %d and %s type %q is level %d; a parent must be exactly one level above its child, so the %s type must be level %d (%s)",
				configured[i].setting, parent.Name, *parent.Level, configured[i+1].setting, child.Name, *child.Level, configured[i].setting, want, hint)
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

// SetStatus moves the card through the Jira statuses mapped to the ralph
// status; a blank or missing mapping does nothing.
func (p *Provider) SetStatus(ctx context.Context, key, status string) error {
	if p.cfg.AssignOnStart && status == string(domain.StatusInProgress) {
		if err := p.assignToTokenUser(ctx, key); err != nil {
			return err
		}
	}
	var raw []string
	if status == string(domain.StatusCancelled) {
		raw = []string{p.cfg.CanceledStatus}
	} else {
		raw = p.cfg.StatusMap[status]
	}
	var path []string
	for _, name := range raw {
		if name = strings.TrimSpace(name); name != "" {
			path = append(path, name)
		}
	}
	if len(path) == 0 {
		return nil
	}
	c, err := p.getClient()
	if err != nil {
		return err
	}
	var prepare func(context.Context) error
	if e := p.cfg.OriginalEstimate; e != (config.JiraEstimates{}) {
		prepare = func(ctx context.Context) error { return c.EnsureOriginalEstimate(ctx, key, p.estimateFor) }
	}
	return c.MoveIssue(ctx, key, path, prepare)
}

// assignToTokenUser assigns an unassigned card to the token's user and never
// overwrites another assignee.
func (p *Provider) assignToTokenUser(ctx context.Context, key string) error {
	c, err := p.getClient()
	if err != nil {
		return err
	}
	me, err := p.tokenUser(ctx, c)
	if err != nil {
		return err
	}
	holder, err := c.Assignee(ctx, key)
	if err != nil {
		return err
	}
	switch {
	case holder == nil:
		return c.AssignIssue(ctx, key, me.AccountID)
	case holder.AccountID == me.AccountID:
		return nil
	}
	name := holder.DisplayName
	if name == "" {
		name = holder.AccountID
	}
	return &taskmanager.AssigneeConflictError{Key: key, Assignee: name}
}

// tokenUser caches only a successful lookup.
func (p *Provider) tokenUser(ctx context.Context, c *Client) (User, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.currentUser != nil {
		return *p.currentUser, nil
	}
	u, err := c.CurrentUser(ctx)
	if err != nil {
		return User{}, err
	}
	p.currentUser = &u
	return u, nil
}

// LinkPullRequest shows the pull request on the card as a remote link.
func (p *Provider) LinkPullRequest(ctx context.Context, key string, l taskmanager.PullRequestLink) error {
	switch {
	case strings.TrimSpace(key) == "":
		return errors.New("jira: link pull request: card key is empty")
	case strings.TrimSpace(l.ID) == "":
		return errors.New("jira: link pull request: link ID is empty")
	case strings.TrimSpace(l.URL) == "":
		return errors.New("jira: link pull request: link URL is empty")
	}
	c, err := p.getClient()
	if err != nil {
		return err
	}
	return c.AddRemoteLink(ctx, key, l)
}

// FindOpenCards lists the open cards in the project that carry every
// configured label. It refuses to run without labels, which would match every
// open card in the project.
func (p *Provider) FindOpenCards(ctx context.Context, search taskmanager.CardSearch) ([]taskmanager.CardCandidate, error) {
	labels := p.labels()
	if len(labels) == 0 {
		return nil, errors.New("task_manager.jira.labels must name at least one label: without labels the search would match every open card in the project")
	}
	c, err := p.getClient()
	if err != nil {
		return nil, err
	}
	found, err := c.SearchOpenCards(ctx, p.cfg.ProjectKey, labels, search.CreatedAfter)
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(p.cfg.BaseURL, "/")
	out := make([]taskmanager.CardCandidate, len(found))
	for i, f := range found {
		out[i] = taskmanager.CardCandidate{
			Key:     f.Key,
			URL:     base + "/browse/" + f.Key,
			Summary: f.Summary,
			Status:  f.Status,
			Created: f.Created,
		}
	}
	return out, nil
}

func (p *Provider) estimateFor(issueType string) string {
	t, e := p.cfg.IssueTypes, p.cfg.OriginalEstimate
	switch {
	case strings.EqualFold(issueType, t.Task):
		return e.Task
	case strings.EqualFold(issueType, t.Milestone):
		return e.Milestone
	case strings.EqualFold(issueType, t.Initiative):
		return e.Initiative
	}
	return ""
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
		Labels:      p.labels(),
		ParentKey:   parent,
	}, nil
}

func (p *Provider) labels() []string {
	var out []string
	for _, l := range p.cfg.Labels {
		if l = strings.TrimSpace(l); l != "" && !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	return out
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
