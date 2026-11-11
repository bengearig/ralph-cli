package bitbucketcloud

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/version"
)

const (
	defaultBaseURL  = "https://api.bitbucket.org/2.0"
	maxResponseBody = 1 << 20
	maxBodySnippet  = 200
)

// Client is a low-level Bitbucket Cloud client. It makes one attempt per call.
type Client struct {
	baseURL   string
	auth      string
	workspace string
	repoSlug  string
	timeout   time.Duration
	http      *http.Client
}

// NewClient reads the credentials named in cfg through lookup. httpClient may
// be nil, in which case a client with the given timeout is used. An empty
// baseURL means the public Bitbucket Cloud API.
func NewClient(cfg config.BitbucketCloudConfig, timeout time.Duration, lookup func(string) (string, bool), httpClient *http.Client, baseURL string) (*Client, error) {
	var auth string
	if cfg.Auth == config.BitbucketAuthBearer {
		token, err := requireEnv(lookup, cfg.TokenEnv, "pr.bitbucket_cloud.token_env")
		if err != nil {
			return nil, err
		}
		auth = "Bearer " + token
	} else {
		email, err := requireEnv(lookup, cfg.EmailEnv, "pr.bitbucket_cloud.email_env")
		if err != nil {
			return nil, err
		}
		token, err := requireEnv(lookup, cfg.TokenEnv, "pr.bitbucket_cloud.token_env")
		if err != nil {
			return nil, err
		}
		auth = "Basic " + base64.StdEncoding.EncodeToString([]byte(email+":"+token))
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	return &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		auth:      auth,
		workspace: cfg.Workspace,
		repoSlug:  cfg.RepoSlug,
		timeout:   timeout,
		http:      httpClient,
	}, nil
}

func requireEnv(lookup func(string) (string, bool), name, setting string) (string, error) {
	v, ok := lookup(name)
	if !ok || strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("environment variable %s (%s) is not set", name, setting)
	}
	return v, nil
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body []byte) ([]byte, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, fmt.Errorf("bitbucket_cloud: %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", c.auth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ralph/"+version.Get().Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bitbucket_cloud: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		return nil, fmt.Errorf("bitbucket_cloud: %s %s: read response: %w", method, path, err)
	}
	if len(data) > maxResponseBody {
		return nil, fmt.Errorf("bitbucket_cloud: %s %s: response exceeds %d bytes", method, path, maxResponseBody)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, c.newAPIError(method, path, resp, data)
	}
	return data, nil
}

// APIError is a non-2xx response from Bitbucket Cloud.
type APIError struct {
	Status     int
	Message    string
	RetryAfter string

	method    string
	path      string
	workspace string
	repoSlug  string
}

func (e *APIError) Error() string {
	prefix := fmt.Sprintf("bitbucket_cloud: %s %s: ", e.method, e.path)
	switch e.Status {
	case http.StatusUnauthorized:
		return prefix + "authentication failed (401); check pr.bitbucket_cloud.auth and the credentials named by pr.bitbucket_cloud.email_env and token_env"
	case http.StatusForbidden:
		return prefix + "permission denied (403); the credential lacks pull request write access"
	case http.StatusNotFound:
		return prefix + fmt.Sprintf("repository %s/%s not found or not accessible (404)", e.workspace, e.repoSlug)
	case http.StatusBadRequest:
		s := prefix + "bad request (400)"
		if e.Message != "" {
			s += ": " + e.Message
		}
		return s + "; check that the source branch has been pushed to the remote"
	case http.StatusTooManyRequests:
		s := prefix + "rate limited (429)"
		if e.RetryAfter != "" {
			s += ", Retry-After: " + e.RetryAfter
		}
		return s
	}
	s := fmt.Sprintf("%s%d %s", prefix, e.Status, http.StatusText(e.Status))
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

func (c *Client) newAPIError(method, path string, resp *http.Response, body []byte) *APIError {
	e := &APIError{
		Status:     resp.StatusCode,
		RetryAfter: strings.TrimSpace(resp.Header.Get("Retry-After")),
		method:     method,
		path:       path,
		workspace:  c.workspace,
		repoSlug:   c.repoSlug,
	}
	var parsed struct {
		Error struct {
			Message string                     `json:"message"`
			Fields  map[string]json.RawMessage `json:"fields"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && (parsed.Error.Message != "" || len(parsed.Error.Fields) > 0) {
		parts := []string{}
		if m := strings.TrimSpace(parsed.Error.Message); m != "" {
			parts = append(parts, m)
		}
		names := make([]string, 0, len(parsed.Error.Fields))
		for k := range parsed.Error.Fields {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			parts = append(parts, k+": "+fieldText(parsed.Error.Fields[k]))
		}
		e.Message = truncate(strings.Join(parts, "; "))
		return e
	}
	e.Message = truncate(strings.TrimSpace(string(body)))
	return e
}

func fieldText(raw json.RawMessage) string {
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return strings.Join(list, ", ")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(bytes.TrimSpace(raw))
}

func truncate(s string) string {
	if len(s) <= maxBodySnippet {
		return s
	}
	return strings.TrimSpace(strings.ToValidUTF8(s[:maxBodySnippet], "")) + "…"
}
