package service

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/embed"
	"github.com/bengearig/ralph/internal/embed/ollama"
	"github.com/bengearig/ralph/internal/project"
	"github.com/bengearig/ralph/internal/search"
	"github.com/bengearig/ralph/internal/store/sqlite"
	"github.com/bengearig/ralph/internal/taskmanager"
)

// NewEmbedder builds the configured embedder; nil means embeddings are off.
func NewEmbedder(c config.EmbeddingsConfig) (embed.Embedder, error) {
	switch c.Provider {
	case config.EmbeddingsNone:
		return nil, nil
	case config.EmbeddingsOllama:
		return ollama.New(ollama.Config{
			BaseURL:        c.Ollama.BaseURL,
			Model:          c.Ollama.Model,
			QueryPrefix:    c.Ollama.QueryPrefix,
			DocumentPrefix: c.Ollama.DocumentPrefix,
			Timeout:        c.Timeout,
		}), nil
	}
	return nil, fmt.Errorf("unknown embeddings provider %q", c.Provider)
}

// Open wires a Service for the project rooted at root. The caller must call
// the returned close function.
func Open(ctx context.Context, cfg *config.Config, root string, log *slog.Logger) (*Service, func() error, error) {
	st, err := sqlite.Open(ctx, cfg.StoragePath(root))
	if err != nil {
		return nil, nil, err
	}
	emb, err := NewEmbedder(cfg.Embeddings)
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	ix := &search.Index{
		Store:    st,
		Embedder: emb,
		Chunker:  search.Chunker{MaxChars: cfg.Embeddings.Chunk.MaxChars, OverlapChars: cfg.Embeddings.Chunk.OverlapChars},
		Timeout:  cfg.Embeddings.Timeout,
		Log:      log,
	}
	svc := New(st, ix, Options{TaskManager: newTaskManager(cfg, log),
		StallThreshold: cfg.Loop.StallThreshold, SearchLimit: cfg.Search.Limit, Log: log,
		LoopLockPath: filepath.Join(root, project.DirName, "loop.lock")})
	return svc, st.Close, nil
}

// newTaskManager builds the configured provider; a build failure is logged and
// leaves cards disabled.
func newTaskManager(cfg *config.Config, log *slog.Logger) taskmanager.Provider {
	p, err := taskmanager.New(cfg)
	if err != nil {
		if log != nil {
			log.Warn("task manager disabled", "error", err)
		}
		return nil
	}
	return p
}
