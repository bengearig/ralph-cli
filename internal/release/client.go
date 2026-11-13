package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	Owner = "bengearig"
	Repo  = "ralph-cli"

	defaultBaseURL = "https://api.github.com"
	apiVersion     = "2022-11-28"
	maxJSONBody    = 1 << 20
	jsonTimeout    = 30 * time.Second
	maxErrSnippet  = 200
)

// ErrNotFound is returned when a release does not exist.
var ErrNotFound = errors.New("release not found")

// Asset is a file attached to a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// Release is a GitHub release.
type Release struct {
	Tag        string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Body       string  `json:"body"`
	Assets     []Asset `json:"assets"`
}

// Asset returns the asset with the given name.
func (r Release) Asset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// Client is an anonymous client for the GitHub releases API.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient returns a Client. An empty baseURL means api.github.com; a nil
// httpClient means a client with no overall timeout.
func NewClient(baseURL string, httpClient *http.Client) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient}
}

// Latest returns the newest stable release.
func (c *Client) Latest(ctx context.Context) (Release, error) {
	var r Release
	err := c.getJSON(ctx, repoPath("releases/latest"), &r)
	return r, err
}

// ByTag returns the release for a tag.
func (c *Client) ByTag(ctx context.Context, tag string) (Release, error) {
	var r Release
	err := c.getJSON(ctx, repoPath("releases/tags/"+url.PathEscape(tag)), &r)
	return r, err
}

// Newest returns the release with the highest version among the first page of
// releases, skipping drafts, tags that are not release versions, and
// pre-releases unless includePre is set.
func (c *Client) Newest(ctx context.Context, includePre bool) (Release, error) {
	var list []Release
	if err := c.getJSON(ctx, repoPath("releases")+"?per_page=100", &list); err != nil {
		return Release{}, err
	}
	var best Release
	var bestVersion Version
	found := false
	for _, r := range list {
		if r.Draft || (r.Prerelease && !includePre) {
			continue
		}
		v, err := ParseVersion(r.Tag)
		if err != nil {
			continue
		}
		if !found || Compare(v, bestVersion) > 0 {
			best, bestVersion, found = r, v, true
		}
	}
	if !found {
		return Release{}, fmt.Errorf("%w: no eligible release in %s/%s", ErrNotFound, Owner, Repo)
	}
	return best, nil
}

// Between returns, oldest first, the releases on the first page of releases
// whose version is greater than after and not greater than the target's,
// skipping drafts, tags that are not release versions, and pre-releases unless
// includePre is set. The target is always included, exactly once.
func (c *Client) Between(ctx context.Context, after Version, target Release, includePre bool) ([]Release, error) {
	targetVersion, err := ParseVersion(target.Tag)
	if err != nil {
		return nil, fmt.Errorf("target release: %w", err)
	}
	var list []Release
	if err := c.getJSON(ctx, repoPath("releases")+"?per_page=100", &list); err != nil {
		return nil, err
	}
	type entry struct {
		release Release
		version Version
	}
	entries := []entry{{target, targetVersion}}
	for _, r := range list {
		if r.Draft || (r.Prerelease && !includePre) || r.Tag == target.Tag {
			continue
		}
		v, err := ParseVersion(r.Tag)
		if err != nil {
			continue
		}
		if Compare(v, after) > 0 && Compare(v, targetVersion) <= 0 {
			entries = append(entries, entry{r, v})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return Compare(entries[i].version, entries[j].version) < 0
	})
	out := make([]Release, len(entries))
	for i, e := range entries {
		out[i] = e.release
	}
	return out, nil
}

// Download streams the body at url to dst and returns its lowercase hex
// SHA-256. It fails when the body exceeds max bytes or the status is not 200.
func (c *Client) Download(ctx context.Context, url string, dst io.Writer, max int64) (string, error) {
	resp, err := c.get(ctx, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", statusError(url, resp)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(resp.Body, max)); err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	var probe [1]byte
	n, err := resp.Body.Read(probe[:])
	if n > 0 {
		return "", fmt.Errorf("download %s: body exceeds %d bytes", url, max)
	}
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func repoPath(rest string) string {
	return "/repos/" + Owner + "/" + Repo + "/" + rest
}

func (c *Client) get(ctx context.Context, target string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("github: GET %s: %w", target, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", "ralph")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: GET %s: %w", target, err)
	}
	return resp, nil
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, jsonTimeout)
	defer cancel()
	resp, err := c.get(ctx, c.baseURL+path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return statusError(path, resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBody+1))
	if err != nil {
		return fmt.Errorf("github: GET %s: read response: %w", path, err)
	}
	if len(data) > maxJSONBody {
		return fmt.Errorf("github: GET %s: response exceeds %d bytes", path, maxJSONBody)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("github: GET %s: decode response: %w", path, err)
	}
	return nil
}

func statusError(what string, resp *http.Response) error {
	code := resp.StatusCode
	if code == http.StatusNotFound {
		return fmt.Errorf("%w: GET %s (404)", ErrNotFound, what)
	}
	if reset, limited := rateLimit(resp); limited {
		msg := fmt.Sprintf("github: GET %s: GitHub rate limit reached (%d)", what, code)
		if !reset.IsZero() {
			msg += "; resets at " + reset.UTC().Format(time.RFC3339)
		}
		return errors.New(msg)
	}
	msg := fmt.Sprintf("github: GET %s: %d %s", what, code, http.StatusText(code))
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrSnippet))
	if s := strings.TrimSpace(strings.ToValidUTF8(string(snippet), "")); s != "" {
		msg += ": " + s
	}
	return errors.New(msg)
}

func rateLimit(resp *http.Response) (time.Time, bool) {
	h := resp.Header
	remaining := strings.TrimSpace(h.Get("X-RateLimit-Remaining"))
	switch resp.StatusCode {
	case http.StatusForbidden:
		if remaining != "0" {
			return time.Time{}, false
		}
	case http.StatusTooManyRequests:
		if remaining == "" && h.Get("X-RateLimit-Reset") == "" && h.Get("Retry-After") == "" {
			return time.Time{}, false
		}
	default:
		return time.Time{}, false
	}
	if secs, err := strconv.ParseInt(strings.TrimSpace(h.Get("X-RateLimit-Reset")), 10, 64); err == nil && secs > 0 {
		return time.Unix(secs, 0), true
	}
	if secs, err := strconv.ParseInt(strings.TrimSpace(h.Get("Retry-After")), 10, 64); err == nil && secs >= 0 {
		return time.Now().Add(time.Duration(secs) * time.Second), true
	}
	return time.Time{}, true
}
