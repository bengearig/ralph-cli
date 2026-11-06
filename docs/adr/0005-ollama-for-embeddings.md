# 5. Ollama for embeddings, degrading gracefully

- Status: Accepted
- Date: 2026-11-06

## Context

Anthropic offers no embeddings API. Hosted options, such as Voyage AI or OpenAI, need keys and send potentially
sensitive business instructions off the machine. A local model keeps data private and works offline.

## Decision

- Embed through Ollama's `POST /api/embed`, with `nomic-embed-text` as the default model. Model names are namespaced
  (`ollama/<model>`) in the index.
- When Ollama is unreachable or the model is missing, entity writes still succeed. Chunks are left stale, the write
  result carries a warning, and search falls back to keywords. `ralph index` backfills stale chunks later.
- `ralph init` checks for the model and prints the `ollama pull` command if it is missing.
- `embeddings.provider: none` disables embeddings entirely.

## Consequences

- Users need Ollama and an embedding model for semantic search; everything else works without them.
- Writes are never lost to an embedding outage.
- Other providers can be added behind `embed.Embedder` without touching callers.
