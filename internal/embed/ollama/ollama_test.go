package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/embed"
)

func server(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestEmbedSuccess(t *testing.T) {
	srv := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.Model != "nomic-embed-text" || len(req.Input) != 2 {
			t.Errorf("request = %+v", req)
		}
		json.NewEncoder(w).Encode(embedResponse{Embeddings: [][]float32{{1, 0}, {0, 1}}})
	})
	c := New(srv.URL+"/", "nomic-embed-text", time.Second)
	vecs, err := c.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 || vecs[1][1] != 1 {
		t.Errorf("vecs = %v", vecs)
	}
	if c.Model() != "ollama/nomic-embed-text" {
		t.Errorf("Model = %q", c.Model())
	}
	if v, err := c.Embed(context.Background(), nil); v != nil || err != nil {
		t.Error("empty input should be a no-op")
	}
}

func TestEmbedErrors(t *testing.T) {
	cases := map[string]struct {
		handler     http.HandlerFunc
		unavailable bool
		contains    string
	}{
		"model missing": {
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`{"error":"model 'nomic-embed-text' not found"}`))
			},
			unavailable: true, contains: "ollama pull nomic-embed-text",
		},
		"server error": {
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotImplemented)
				w.Write([]byte(`{"error":"no embeddings"}`))
			},
			unavailable: true, contains: "no embeddings",
		},
		"malformed json": {
			handler:  func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"embeddings":`)) },
			contains: "decoding",
		},
		"count mismatch": {
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Write([]byte(`{"embeddings":[[1,2]]}`))
			},
			contains: "got 1 embeddings for 2 inputs",
		},
		"dimension mismatch": {
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Write([]byte(`{"embeddings":[[1,2],[1]]}`))
			},
			contains: "dimensions",
		},
		"timeout": {
			handler: func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-time.After(500 * time.Millisecond):
				case <-r.Context().Done():
				}
			},
			unavailable: true, contains: "timed out",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := server(t, tc.handler)
			c := New(srv.URL, "nomic-embed-text", 100*time.Millisecond)
			_, err := c.Embed(context.Background(), []string{"a", "b"})
			if err == nil {
				t.Fatal("expected error")
			}
			if errors.Is(err, embed.ErrUnavailable) != tc.unavailable {
				t.Errorf("ErrUnavailable = %v for %v", !tc.unavailable, err)
			}
			if !strings.Contains(err.Error(), tc.contains) {
				t.Errorf("error %q should contain %q", err, tc.contains)
			}
		})
	}
}

func TestEmbedConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := New(url, "m", time.Second).Embed(context.Background(), []string{"a"})
	if !errors.Is(err, embed.ErrUnavailable) {
		t.Errorf("err = %v", err)
	}
}

func TestHasModel(t *testing.T) {
	srv := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Write([]byte(`{"models":[{"name":"nomic-embed-text:latest","model":"nomic-embed-text:latest"},{"name":"mxbai-embed-large:335m"}]}`))
	})
	for model, want := range map[string]bool{
		"nomic-embed-text":        true,
		"nomic-embed-text:latest": true,
		"mxbai-embed-large:335m":  true,
		"mxbai-embed-large":       false,
		"llama3.1":                false,
	} {
		got, err := New(srv.URL, model, time.Second).HasModel(context.Background())
		if err != nil || got != want {
			t.Errorf("HasModel(%s) = %v, %v; want %v", model, got, err, want)
		}
	}
}
