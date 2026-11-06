// Package ollama embeds text with a local Ollama server's /api/embed endpoint.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bengearig/ralph/internal/embed"
)

// Auto selects the prefix a known model expects, or none for other models.
const Auto = "auto"

// knownPrefixes maps model base names to the task prefixes their authors
// require for retrieval.
var knownPrefixes = map[string]struct{ query, document string }{
	"nomic-embed-text": {query: "search_query: ", document: "search_document: "},
}

// Config describes an Ollama embedding client.
type Config struct {
	BaseURL string
	Model   string
	// QueryPrefix and DocumentPrefix are prepended to inputs by purpose.
	// Auto resolves them from the model; any other value, including "", is
	// used literally.
	QueryPrefix    string
	DocumentPrefix string
	// Timeout bounds each request.
	Timeout time.Duration
}

// Client calls Ollama's embedding API.
type Client struct {
	baseURL     string
	model       string
	queryPrefix string
	docPrefix   string
	http        *http.Client
}

var _ embed.Embedder = (*Client)(nil)

// New returns a client for cfg, resolving Auto prefixes.
func New(cfg Config) *Client {
	query, doc := ResolvePrefixes(cfg.Model, cfg.QueryPrefix, cfg.DocumentPrefix)
	return &Client{
		baseURL:     strings.TrimRight(cfg.BaseURL, "/"),
		model:       cfg.Model,
		queryPrefix: query,
		docPrefix:   doc,
		http:        &http.Client{Timeout: cfg.Timeout},
	}
}

// ResolvePrefixes returns the effective query and document prefixes for
// model, replacing each Auto with the model's known prefix or "".
func ResolvePrefixes(model, query, document string) (string, string) {
	known := knownPrefixes[baseName(model)]
	if query == Auto {
		query = known.query
	}
	if document == Auto {
		document = known.document
	}
	return query, document
}

// baseName strips a registry namespace and tag: "library/nomic-embed-text:v1.5"
// becomes "nomic-embed-text".
func baseName(model string) string {
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	if i := strings.Index(model, ":"); i >= 0 {
		model = model[:i]
	}
	return model
}

// Model identifies the vector space of stored embeddings. The document prefix
// is part of it, because changing the prefix changes every stored vector.
func (c *Client) Model() string {
	id := "ollama/" + c.model
	if c.docPrefix != "" {
		id += " document_prefix=" + strconv.Quote(c.docPrefix)
	}
	return id
}

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// Embed embeds texts in one request, each prefixed for purpose. Connection
// failures wrap embed.ErrUnavailable; a missing model is reported with a pull
// hint.
func (c *Client) Embed(ctx context.Context, purpose embed.Purpose, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	var prefix string
	switch purpose {
	case embed.Document:
		prefix = c.docPrefix
	case embed.Query:
		prefix = c.queryPrefix
	default:
		return nil, fmt.Errorf("ollama: unknown embedding purpose %d", purpose)
	}
	input := texts
	if prefix != "" {
		input = make([]string, len(texts))
		for i, t := range texts {
			input[i] = prefix + t
		}
	}
	body, err := json.Marshal(embedRequest{Model: c.model, Input: input})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: ollama at %s: %v", embed.ErrUnavailable, c.baseURL, unwrapURLError(err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: ollama: reading response: %v", embed.ErrUnavailable, err)
	}
	if resp.StatusCode != http.StatusOK {
		var e errorResponse
		msg := strings.TrimSpace(string(raw))
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		if resp.StatusCode == http.StatusNotFound && strings.Contains(msg, "not found") {
			return nil, fmt.Errorf("%w: ollama model %q is not installed (run `ollama pull %s`): %s",
				embed.ErrUnavailable, c.model, c.model, msg)
		}
		return nil, fmt.Errorf("%w: ollama returned %s: %s", embed.ErrUnavailable, resp.Status, msg)
	}
	var out embedResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("ollama: decoding response: %w", err)
	}
	if len(out.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama: got %d embeddings for %d inputs", len(out.Embeddings), len(texts))
	}
	dims := len(out.Embeddings[0])
	for i, v := range out.Embeddings {
		if len(v) == 0 || len(v) != dims {
			return nil, fmt.Errorf("ollama: embedding %d has %d dimensions, expected %d", i, len(v), dims)
		}
	}
	return out.Embeddings, nil
}

// HasModel reports whether the server has the configured model installed.
func (c *Client) HasModel(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/tags", nil)
	if err != nil {
		return false, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("%w: ollama at %s: %v", embed.ErrUnavailable, c.baseURL, unwrapURLError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("%w: ollama returned %s", embed.ErrUnavailable, resp.Status)
	}
	var tags struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return false, fmt.Errorf("ollama: decoding tags: %w", err)
	}
	want := []string{c.model}
	if !strings.Contains(c.model, ":") {
		want = append(want, c.model+":latest")
	}
	for _, m := range tags.Models {
		if slices.Contains(want, m.Name) || slices.Contains(want, m.Model) {
			return true, nil
		}
	}
	return false, nil
}

// ModelName is the un-namespaced model, as Ollama knows it.
func (c *Client) ModelName() string { return c.model }

func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		var ne net.Error
		if errors.As(ue.Err, &ne) && ne.Timeout() {
			return errors.New("request timed out")
		}
		return ue.Err
	}
	return err
}
