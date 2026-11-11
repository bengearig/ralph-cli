// Package bitbucketcloud opens pull requests on Bitbucket Cloud
// (bitbucket.org, REST API 2.0).
package bitbucketcloud

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
const Name = config.PRBitbucketCloud

func init() {
	pr.Register(Name, func(cfg *config.Config) (pr.Provider, error) {
		return New(cfg.PR.BitbucketCloud, cfg.PR.Timeout, os.LookupEnv), nil
	})
	pr.RegisterLink(Name, link)
}

func link(namespace, repository string, number int) string {
	return fmt.Sprintf("https://bitbucket.org/%s/%s/pull-requests/%d",
		url.PathEscape(namespace), url.PathEscape(repository), number)
}

// Provider targets one Bitbucket Cloud repository.
type Provider struct {
	cfg     config.BitbucketCloudConfig
	timeout time.Duration
	lookup  func(string) (string, bool)
	http    *http.Client
	baseURL string
}

var _ pr.Provider = (*Provider)(nil)

// New returns a provider for the public Bitbucket Cloud API; lookup reads
// environment variables.
func New(cfg config.BitbucketCloudConfig, timeout time.Duration, lookup func(string) (string, bool)) *Provider {
	return NewWithClient(cfg, timeout, lookup, nil, "")
}

// NewWithClient is New with an explicit HTTP client and API base URL; either
// may be zero to use the default.
func NewWithClient(cfg config.BitbucketCloudConfig, timeout time.Duration, lookup func(string) (string, bool), httpClient *http.Client, baseURL string) *Provider {
	return &Provider{cfg: cfg, timeout: timeout, lookup: lookup, http: httpClient, baseURL: baseURL}
}

func (p *Provider) Name() string { return Name }

// Validate reports every missing setting at once.
func (p *Provider) Validate() error {
	var problems []error
	if strings.TrimSpace(p.cfg.Workspace) == "" {
		problems = append(problems, errors.New("pr.bitbucket_cloud.workspace is not set"))
	}
	if strings.TrimSpace(p.cfg.RepoSlug) == "" {
		problems = append(problems, errors.New("pr.bitbucket_cloud.repo_slug is not set"))
	}
	if p.cfg.Auth != config.BitbucketAuthBearer {
		if _, err := requireEnv(p.lookup, p.cfg.EmailEnv, "pr.bitbucket_cloud.email_env"); err != nil {
			problems = append(problems, err)
		}
	}
	if _, err := requireEnv(p.lookup, p.cfg.TokenEnv, "pr.bitbucket_cloud.token_env"); err != nil {
		problems = append(problems, err)
	}
	return errors.Join(problems...)
}

// Create opens a pull request on the configured repository.
func (p *Provider) Create(ctx context.Context, req pr.Request) (pr.PullRequest, error) {
	if err := p.Validate(); err != nil {
		return pr.PullRequest{}, err
	}
	client, err := NewClient(p.cfg, p.timeout, p.lookup, p.http, p.baseURL)
	if err != nil {
		return pr.PullRequest{}, err
	}
	type branch struct {
		Branch struct {
			Name string `json:"name"`
		} `json:"branch"`
	}
	payload := struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Source      branch `json:"source"`
		Destination branch `json:"destination"`
		Draft       bool   `json:"draft"`
	}{Title: req.Title, Description: req.Body, Draft: req.Draft}
	payload.Source.Branch.Name = req.SourceBranch
	payload.Destination.Branch.Name = req.DestinationBranch
	body, err := json.Marshal(payload)
	if err != nil {
		return pr.PullRequest{}, fmt.Errorf("bitbucket_cloud: encode pull request: %w", err)
	}
	path := fmt.Sprintf("/repositories/%s/%s/pullrequests", url.PathEscape(p.cfg.Workspace), url.PathEscape(p.cfg.RepoSlug))
	data, err := client.do(ctx, http.MethodPost, path, nil, body)
	if err != nil {
		return pr.PullRequest{}, err
	}
	var resp struct {
		ID    int `json:"id"`
		Links struct {
			HTML struct {
				Href string `json:"href"`
			} `json:"html"`
		} `json:"links"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return pr.PullRequest{}, fmt.Errorf("bitbucket_cloud: POST %s: decode response: %w", path, err)
	}
	if resp.ID <= 0 {
		return pr.PullRequest{}, fmt.Errorf("bitbucket_cloud: POST %s: response has no pull request id", path)
	}
	href := resp.Links.HTML.Href
	if href == "" {
		href = link(p.cfg.Workspace, p.cfg.RepoSlug, resp.ID)
	}
	return pr.PullRequest{
		ID:         fmt.Sprint(resp.ID),
		Number:     resp.ID,
		Namespace:  p.cfg.Workspace,
		Repository: p.cfg.RepoSlug,
		URL:        href,
	}, nil
}
