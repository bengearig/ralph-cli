// Package bitbucketcloud opens pull requests on Bitbucket Cloud
// (bitbucket.org, REST API 2.0).
package bitbucketcloud

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/pr"
)

// Name is the provider's config identifier.
const Name = config.PRBitbucketCloud

func init() {
	pr.Register(Name, func(cfg *config.Config) (pr.Provider, error) {
		return New(cfg.PR.BitbucketCloud, os.LookupEnv), nil
	})
}

// Provider targets one Bitbucket Cloud repository.
type Provider struct {
	cfg    config.BitbucketCloudConfig
	lookup func(string) (string, bool)
}

var _ pr.Provider = (*Provider)(nil)

// New returns a provider; lookup reads environment variables.
func New(cfg config.BitbucketCloudConfig, lookup func(string) (string, bool)) *Provider {
	return &Provider{cfg: cfg, lookup: lookup}
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
	if v, ok := p.lookup(p.cfg.TokenEnv); !ok || strings.TrimSpace(v) == "" {
		problems = append(problems, fmt.Errorf("environment variable %s (pr.bitbucket_cloud.token_env) is not set", p.cfg.TokenEnv))
	}
	return errors.Join(problems...)
}

// Create is not implemented yet.
func (p *Provider) Create(context.Context, pr.Request) (pr.PullRequest, error) {
	if err := p.Validate(); err != nil {
		return pr.PullRequest{}, err
	}
	return pr.PullRequest{}, fmt.Errorf("bitbucket_cloud pull request creation: %w", errs.ErrNotImplemented)
}
