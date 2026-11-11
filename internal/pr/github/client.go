package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/version"
)

const (
	defaultBaseURL  = "https://api.github.com"
	apiVersion      = "2022-11-28"
	maxResponseBody = 1 << 20
	maxBodySnippet  = 200
)

// Client is a low-level GitHub REST client. It makes one attempt per call.
type Client struct {
	baseURL string
	token   string
	owner   string
	repo    string
	timeout time.Duration
	http    *http.Client
}

// NewClient reads the token from the variable named by cfg.TokenEnv through
// lookup. httpClient may be nil, in which case a client with the given timeout
// is used. An empty baseURL means api.github.com.
func NewClient(cfg config.GitHubConfig, timeout time.Duration, lookup func(string) (string, bool), httpClient *http.Client, baseURL string) (*Client, error) {
	token, ok := lookup(cfg.TokenEnv)
	if !ok || strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("the environment variable named by pr.github.token_env is not set or is blank")
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		owner:   cfg.Owner,
		repo:    cfg.Repo,
		timeout: timeout,
		http:    httpClient,
	}, nil
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
		return nil, fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", "ralph/"+version.Get().Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		return nil, fmt.Errorf("github: %s %s: read response: %w", method, path, err)
	}
	if len(data) > maxResponseBody {
		return nil, fmt.Errorf("github: %s %s: response exceeds %d bytes", method, path, maxResponseBody)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, c.newAPIError(method, path, resp, data)
	}
	return data, nil
}

// APIError is a non-2xx response from GitHub.
type APIError struct {
	Status  int
	Message string
	// RateLimited is set on a 403 whose X-RateLimit-Remaining is 0.
	RateLimited bool
	// ResetAt is the X-RateLimit-Reset time, when GitHub sent one.
	ResetAt    time.Time
	RetryAfter string

	method string
	path   string
	owner  string
	repo   string
}

func (e *APIError) Error() string {
	prefix := fmt.Sprintf("github: %s %s: ", e.method, e.path)
	switch e.Status {
	case http.StatusUnauthorized:
		return prefix + "bad credentials (401); check the token in the environment variable named by pr.github.token_env"
	case http.StatusForbidden:
		if e.RateLimited {
			return prefix + "rate limited (403)" + e.resetSuffix()
		}
		s := prefix + "permission denied (403); the token lacks pull request write access"
		if e.Message != "" {
			s += ": " + e.Message
		}
		return s
	case http.StatusNotFound:
		return prefix + fmt.Sprintf("repository %s/%s not found or the token cannot see it (404)", e.owner, e.repo)
	case http.StatusUnprocessableEntity:
		s := prefix + "validation failed (422)"
		if e.Message != "" {
			s += ": " + e.Message
		}
		s += "; usual causes are a head branch that is not on the remote, no commits between the branches, or an existing pull request"
		if strings.Contains(strings.ToLower(e.Message), "draft pull requests are not supported") {
			s += "; set pr.draft: false"
		}
		return s
	case http.StatusTooManyRequests:
		s := prefix + "rate limited (429)" + e.resetSuffix()
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

func (e *APIError) resetSuffix() string {
	if e.ResetAt.IsZero() {
		return ""
	}
	return ", resets at " + e.ResetAt.UTC().Format(time.RFC3339)
}

func (c *Client) newAPIError(method, path string, resp *http.Response, body []byte) *APIError {
	e := &APIError{
		Status:     resp.StatusCode,
		RetryAfter: strings.TrimSpace(resp.Header.Get("Retry-After")),
		method:     method,
		path:       path,
		owner:      c.owner,
		repo:       c.repo,
	}
	if strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining")) == "0" {
		e.RateLimited = resp.StatusCode == http.StatusForbidden
		if secs, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset")), 10, 64); err == nil && secs > 0 {
			e.ResetAt = time.Unix(secs, 0)
		}
	}

	var parsed struct {
		Message string            `json:"message"`
		Errors  []json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && (strings.TrimSpace(parsed.Message) != "" || len(parsed.Errors) > 0) {
		parts := []string{}
		if m := strings.TrimSpace(parsed.Message); m != "" {
			parts = append(parts, m)
		}
		for _, raw := range parsed.Errors {
			if t := errorEntryText(raw); t != "" {
				parts = append(parts, t)
			}
		}
		e.Message = truncate(strings.Join(parts, "; "))
		return e
	}
	e.Message = truncate(strings.TrimSpace(string(body)))
	return e
}

func errorEntryText(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var entry struct {
		Resource string `json:"resource"`
		Field    string `json:"field"`
		Code     string `json:"code"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil {
		return string(bytes.TrimSpace(raw))
	}
	if m := strings.TrimSpace(entry.Message); m != "" {
		return m
	}
	var fields []string
	for _, f := range []string{entry.Resource, entry.Field, entry.Code} {
		if f != "" {
			fields = append(fields, f)
		}
	}
	return strings.Join(fields, " ")
}

func truncate(s string) string {
	if len(s) <= maxBodySnippet {
		return s
	}
	return strings.TrimSpace(strings.ToValidUTF8(s[:maxBodySnippet], "")) + "…"
}
