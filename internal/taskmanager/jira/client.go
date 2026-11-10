// Package jira talks to Jira Cloud (REST API v3) for the task manager.
package jira

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
)

const (
	issuePath       = "/rest/api/3/issue"
	projectPath     = "/rest/api/3/project"
	maxResponseBody = 1 << 20
	maxBodySnippet  = 200
)

// Client is a low-level Jira Cloud client. It makes one attempt per call.
type Client struct {
	baseURL string
	auth    string
	timeout time.Duration
	http    *http.Client
}

// NewClient reads the credentials named in cfg through lookup. httpClient may
// be nil, in which case a client with the given timeout is used.
func NewClient(cfg config.JiraConfig, timeout time.Duration, lookup func(string) (string, bool), httpClient *http.Client) (*Client, error) {
	email, err := requireEnv(lookup, cfg.EmailEnv, "task_manager.jira.email_env")
	if err != nil {
		return nil, err
	}
	token, err := requireEnv(lookup, cfg.TokenEnv, "task_manager.jira.token_env")
	if err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	return &Client{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		auth:    "Basic " + base64.StdEncoding.EncodeToString([]byte(email+":"+token)),
		timeout: timeout,
		http:    httpClient,
	}, nil
}

func requireEnv(lookup func(string) (string, bool), name, setting string) (string, error) {
	v, ok := lookup(name)
	if !ok || strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("environment variable %s (%s) is not set", name, setting)
	}
	return v, nil
}

// IssueFields are the fields ralph sets on a card. Description is an ADF
// document, or empty for none.
type IssueFields struct {
	Project     string
	IssueType   string
	Summary     string
	Description json.RawMessage
	Labels      []string
	ParentKey   string
}

type keyRef struct {
	Key string `json:"key,omitempty"`
}

type nameRef struct {
	Name string `json:"name"`
}

type createFields struct {
	Project     keyRef          `json:"project"`
	IssueType   nameRef         `json:"issuetype"`
	Summary     string          `json:"summary"`
	Description json.RawMessage `json:"description,omitempty"`
	Labels      []string        `json:"labels,omitempty"`
	Parent      *keyRef         `json:"parent,omitempty"`
}

// CreateIssue creates a card and returns its key.
func (c *Client) CreateIssue(ctx context.Context, f IssueFields) (string, error) {
	cf := createFields{
		Project:     keyRef{Key: f.Project},
		IssueType:   nameRef{Name: f.IssueType},
		Summary:     f.Summary,
		Description: f.Description,
		Labels:      f.Labels,
	}
	if len(bytes.TrimSpace(f.Description)) == 0 {
		cf.Description = nil
	}
	if f.ParentKey != "" {
		cf.Parent = &keyRef{Key: f.ParentKey}
	}
	body, err := json.Marshal(struct {
		Fields createFields `json:"fields"`
	}{cf})
	if err != nil {
		return "", fmt.Errorf("jira: encode create request: %w", err)
	}
	respBody, err := c.do(ctx, http.MethodPost, issuePath, body)
	if err != nil {
		return "", err
	}
	var resp struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return "", fmt.Errorf("jira: POST %s: response is not valid JSON: %w", issuePath, err)
	}
	if resp.Key == "" {
		return "", fmt.Errorf("jira: POST %s: response has no issue key", issuePath)
	}
	return resp.Key, nil
}

// UpdateIssue overwrites a card's summary and description. An empty
// description clears the card's description.
func (c *Client) UpdateIssue(ctx context.Context, key string, f IssueFields) error {
	desc := f.Description
	if len(bytes.TrimSpace(desc)) == 0 {
		desc = json.RawMessage("null")
	}
	body, err := json.Marshal(struct {
		Fields struct {
			Summary     string          `json:"summary"`
			Description json.RawMessage `json:"description"`
		} `json:"fields"`
	}{Fields: struct {
		Summary     string          `json:"summary"`
		Description json.RawMessage `json:"description"`
	}{f.Summary, desc}})
	if err != nil {
		return fmt.Errorf("jira: encode update request: %w", err)
	}
	_, err = c.do(ctx, http.MethodPut, issuePath+"/"+url.PathEscape(key), body)
	return err
}

// IssueType is an issue type of a project. Level is nil when Jira did not report a hierarchy level.
type IssueType struct {
	Name  string
	Level *int
}

// ProjectIssueTypes lists the issue types of the configured project with their hierarchy levels.
func (c *Client) ProjectIssueTypes(ctx context.Context, projectKey string) ([]IssueType, error) {
	path := projectPath + "/" + url.PathEscape(projectKey)
	data, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		IssueTypes []struct {
			Name           string `json:"name"`
			HierarchyLevel *int   `json:"hierarchyLevel"`
		} `json:"issueTypes"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("jira: GET %s: response is not valid JSON: %w", path, err)
	}
	out := make([]IssueType, 0, len(resp.IssueTypes))
	for _, t := range resp.IssueTypes {
		out = append(out, IssueType{Name: t.Name, Level: t.HierarchyLevel})
	}
	return out, nil
}

func (c *Client) issueStatus(ctx context.Context, key string) (string, error) {
	path := issuePath + "/" + url.PathEscape(key)
	data, err := c.do(ctx, http.MethodGet, path+"?fields=status", nil)
	if err != nil {
		return "", err
	}
	var issue struct {
		Fields struct {
			Status struct {
				Name string `json:"name"`
			} `json:"status"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(data, &issue); err != nil {
		return "", fmt.Errorf("jira: GET %s: response is not valid JSON: %w", path, err)
	}
	return issue.Fields.Status.Name, nil
}

// MoveIssue moves a card through the statuses of path in order. It starts after
// the card's current status when that is in path, so a card part way along
// resumes. It stops at the first step that fails, leaving the card where it got
// to. prepare, when set, runs once before the first step, and only when there is one.
func (c *Client) MoveIssue(ctx context.Context, key string, path []string, prepare func(context.Context) error) error {
	current, err := c.issueStatus(ctx, key)
	if err != nil {
		return err
	}
	steps := path
	for i, name := range path {
		if strings.EqualFold(name, current) {
			steps = path[i+1:]
			break
		}
	}
	if len(steps) == 0 {
		return nil
	}
	if prepare != nil {
		if err := prepare(ctx); err != nil {
			return err
		}
	}
	for _, name := range steps {
		if err := c.transition(ctx, key, current, name); err != nil {
			if len(path) > 1 {
				err = fmt.Errorf("%w (moving through %s)", err, strings.Join(path, " > "))
			}
			return err
		}
		current = name
	}
	return nil
}

// EnsureOriginalEstimate sets the card's original estimate to what pick returns for its
// issue type, unless it already has one or pick returns "". Jira workflows can require it.
func (c *Client) EnsureOriginalEstimate(ctx context.Context, key string, pick func(issueType string) string) error {
	path := issuePath + "/" + url.PathEscape(key)
	data, err := c.do(ctx, http.MethodGet, path+"?fields=issuetype,timetracking", nil)
	if err != nil {
		return err
	}
	var issue struct {
		Fields struct {
			IssueType struct {
				Name string `json:"name"`
			} `json:"issuetype"`
			TimeTracking struct {
				OriginalEstimate        string `json:"originalEstimate"`
				OriginalEstimateSeconds int64  `json:"originalEstimateSeconds"`
			} `json:"timetracking"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(data, &issue); err != nil {
		return fmt.Errorf("jira: GET %s: response is not valid JSON: %w", path, err)
	}
	tt := issue.Fields.TimeTracking
	if tt.OriginalEstimate != "" || tt.OriginalEstimateSeconds > 0 {
		return nil
	}
	estimate := pick(issue.Fields.IssueType.Name)
	if estimate == "" {
		return nil
	}
	body, err := json.Marshal(map[string]any{"fields": map[string]any{"timetracking": map[string]string{"originalEstimate": estimate}}})
	if err != nil {
		return fmt.Errorf("jira: encode estimate request: %w", err)
	}
	_, err = c.do(ctx, http.MethodPut, path, body)
	return err
}

// TransitionIssue moves a card to the status named statusName, choosing the
// workflow transition that leads to it. It does nothing when the card is
// already there.
func (c *Client) TransitionIssue(ctx context.Context, key, statusName string) error {
	current, err := c.issueStatus(ctx, key)
	if err != nil {
		return err
	}
	return c.transition(ctx, key, current, statusName)
}

func (c *Client) transition(ctx context.Context, key, current, statusName string) error {
	if strings.EqualFold(current, statusName) {
		return nil
	}
	tpath := issuePath + "/" + url.PathEscape(key) + "/transitions"
	data, err := c.do(ctx, http.MethodGet, tpath, nil)
	if err != nil {
		return err
	}
	var list struct {
		Transitions []struct {
			ID string `json:"id"`
			To struct {
				Name string `json:"name"`
			} `json:"to"`
		} `json:"transitions"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("jira: GET %s: response is not valid JSON: %w", tpath, err)
	}
	reachable := make([]string, 0, len(list.Transitions))
	for _, t := range list.Transitions {
		if strings.EqualFold(t.To.Name, statusName) {
			body, err := json.Marshal(map[string]any{"transition": map[string]string{"id": t.ID}})
			if err != nil {
				return fmt.Errorf("jira: encode transition request: %w", err)
			}
			_, err = c.do(ctx, http.MethodPost, tpath, body)
			return err
		}
		reachable = append(reachable, t.To.Name)
	}
	sort.Strings(reachable)
	names := "none"
	if len(reachable) > 0 {
		names = strings.Join(reachable, ", ")
	}
	return fmt.Errorf("jira: %s: no transition to status %q; reachable statuses: %s", key, statusName, names)
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("jira: %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", c.auth)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jira: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return nil, fmt.Errorf("jira: %s %s: read response: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, newAPIError(method, path, resp, data)
	}
	return data, nil
}

// APIError is a non-2xx response from Jira.
type APIError struct {
	Status     int
	Messages   []string
	RetryAfter string

	method string
	path   string
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "jira: %s %s: %d %s", e.method, e.path, e.Status, http.StatusText(e.Status))
	if len(e.Messages) > 0 {
		b.WriteString(": " + strings.Join(e.Messages, "; "))
	}
	if h := e.hint(); h != "" {
		b.WriteString("; " + h)
	}
	return b.String()
}

func (e *APIError) hint() string {
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "check the email and API token, and that the token's user can create issues in the project"
	case http.StatusNotFound:
		return "check task_manager.jira.base_url and project_key"
	case http.StatusBadRequest:
		text := strings.ToLower(strings.Join(e.Messages, " "))
		if strings.Contains(text, "parent") {
			return "check that each configured issue type sits exactly one hierarchy level below its parent's type (task_manager.jira.issue_types)"
		}
		for _, w := range []string{"issuetype", "issue type", "field", "required"} {
			if strings.Contains(text, w) {
				return "check task_manager.jira.issue_types against the project's issue types"
			}
		}
	case http.StatusTooManyRequests:
		if e.RetryAfter != "" {
			return "rate limited, Retry-After: " + e.RetryAfter
		}
		return "rate limited"
	}
	return ""
}

func newAPIError(method, path string, resp *http.Response, body []byte) *APIError {
	e := &APIError{
		Status:     resp.StatusCode,
		RetryAfter: resp.Header.Get("Retry-After"),
		method:     method,
		path:       path,
	}
	var parsed struct {
		ErrorMessages []string          `json:"errorMessages"`
		Errors        map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		e.Messages = append(e.Messages, parsed.ErrorMessages...)
		fields := make([]string, 0, len(parsed.Errors))
		for k := range parsed.Errors {
			fields = append(fields, k)
		}
		sort.Strings(fields)
		for _, k := range fields {
			e.Messages = append(e.Messages, k+": "+parsed.Errors[k])
		}
		return e
	}
	if s := strings.TrimSpace(string(body)); s != "" {
		if len(s) > maxBodySnippet {
			s = strings.TrimSpace(strings.ToValidUTF8(s[:maxBodySnippet], "")) + "…"
		}
		e.Messages = []string{s}
	}
	return e
}
