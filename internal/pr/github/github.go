// Package github opens pull requests on GitHub (github.com, REST API).
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/pr"
)

// Name is the provider's config identifier.
const Name = config.PRGitHub

func init() {
	pr.Register(Name, func(cfg *config.Config) (pr.Provider, error) {
		return New(cfg.PR.GitHub, cfg.PR.Timeout, os.LookupEnv), nil
	})
	pr.RegisterLink(Name, link)
}

func link(namespace, repository string, number int) string {
	return fmt.Sprintf("https://github.com/%s/%s/pull/%d",
		url.PathEscape(namespace), url.PathEscape(repository), number)
}

// Provider targets one GitHub repository.
type Provider struct {
	cfg     config.GitHubConfig
	timeout time.Duration
	lookup  func(string) (string, bool)
	http    *http.Client
	baseURL string
}

var _ pr.Provider = (*Provider)(nil)

// New returns a provider for the public GitHub API; lookup reads environment
// variables.
func New(cfg config.GitHubConfig, timeout time.Duration, lookup func(string) (string, bool)) *Provider {
	return NewWithClient(cfg, timeout, lookup, nil, "")
}

// NewWithClient is New with an explicit HTTP client and API base URL; either
// may be zero to use the default.
func NewWithClient(cfg config.GitHubConfig, timeout time.Duration, lookup func(string) (string, bool), httpClient *http.Client, baseURL string) *Provider {
	return &Provider{cfg: cfg, timeout: timeout, lookup: lookup, http: httpClient, baseURL: baseURL}
}

func (p *Provider) Name() string { return Name }

// Validate reports every missing setting at once.
func (p *Provider) Validate() error {
	var problems []error
	if strings.TrimSpace(p.cfg.Owner) == "" {
		problems = append(problems, errors.New("pr.github.owner is not set"))
	}
	if strings.TrimSpace(p.cfg.Repo) == "" {
		problems = append(problems, errors.New("pr.github.repo is not set"))
	}
	if strings.TrimSpace(p.cfg.TokenEnv) == "" {
		problems = append(problems, errors.New("pr.github.token_env is not set"))
	} else if v, ok := p.lookup(p.cfg.TokenEnv); !ok || strings.TrimSpace(v) == "" {
		problems = append(problems, fmt.Errorf("pr.github.token_env: environment variable %s is not set or is blank", p.cfg.TokenEnv))
	}
	return errors.Join(problems...)
}

func (p *Provider) client() (*Client, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return NewClient(p.cfg, p.timeout, p.lookup, p.http, p.baseURL)
}

func (p *Provider) pullsPath() string {
	return fmt.Sprintf("/repos/%s/%s/pulls", url.PathEscape(p.cfg.Owner), url.PathEscape(p.cfg.Repo))
}

// Create opens a pull request on the configured repository.
func (p *Provider) Create(ctx context.Context, req pr.Request) (pr.PullRequest, error) {
	client, err := p.client()
	if err != nil {
		return pr.PullRequest{}, err
	}
	body, err := json.Marshal(struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		Head  string `json:"head"`
		Base  string `json:"base"`
		Draft bool   `json:"draft"`
	}{req.Title, req.Body, req.SourceBranch, req.DestinationBranch, req.Draft})
	if err != nil {
		return pr.PullRequest{}, fmt.Errorf("github: encode pull request: %w", err)
	}
	path := p.pullsPath()
	data, err := client.do(ctx, http.MethodPost, path, nil, body)
	if err != nil {
		return pr.PullRequest{}, err
	}
	var resp apiPullRequest
	if err := json.Unmarshal(data, &resp); err != nil {
		return pr.PullRequest{}, fmt.Errorf("github: POST %s: decode response: %w", path, err)
	}
	if resp.Number <= 0 {
		return pr.PullRequest{}, fmt.Errorf("github: POST %s: response has no pull request number", path)
	}
	return p.pullRequest(resp), nil
}

// apiPullRequest is the part of a GitHub pull request ralph reads.
type apiPullRequest struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
}

func (p *Provider) pullRequest(resp apiPullRequest) pr.PullRequest {
	href := resp.HTMLURL
	if href == "" {
		href = link(p.cfg.Owner, p.cfg.Repo, resp.Number)
	}
	return pr.PullRequest{
		ID:         fmt.Sprint(resp.Number),
		Number:     resp.Number,
		Namespace:  p.cfg.Owner,
		Repository: p.cfg.Repo,
		URL:        href,
	}
}

// FindOpen returns the most recently updated open pull request from
// sourceBranch into destinationBranch.
func (p *Provider) FindOpen(ctx context.Context, sourceBranch, destinationBranch string) (pr.PullRequest, bool, error) {
	client, err := p.client()
	if err != nil {
		return pr.PullRequest{}, false, err
	}
	query := url.Values{
		"state":     {"open"},
		"head":      {p.cfg.Owner + ":" + sourceBranch},
		"base":      {destinationBranch},
		"sort":      {"updated"},
		"direction": {"desc"},
		"per_page":  {"1"},
	}
	path := p.pullsPath()
	data, err := client.do(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return pr.PullRequest{}, false, err
	}
	var resp []apiPullRequest
	if err := json.Unmarshal(data, &resp); err != nil {
		return pr.PullRequest{}, false, fmt.Errorf("github: GET %s: decode response: %w", path, err)
	}
	if len(resp) == 0 {
		return pr.PullRequest{}, false, nil
	}
	if resp[0].Number <= 0 {
		return pr.PullRequest{}, false, fmt.Errorf("github: GET %s: response has no pull request number", path)
	}
	return p.pullRequest(resp[0]), true, nil
}
