# 12. Embedding task prefixes, part of the model identity

- Status: Accepted
- Date: 2026-11-06
- Refines: [5. Ollama for embeddings](0005-ollama-for-embeddings.md)

## Context

`nomic-embed-text`, the default embedding model, is trained to receive a task prefix: `search_document: ` for
content being indexed and `search_query: ` for queries. Without the prefixes, its similarity scores bunch together
and retrieval suffers. Other models use different prefixes or none, so a fixed prefix would be wrong after a model
change. Vectors embedded with one document prefix are not comparable with vectors embedded with another.

## Decision

- `embed.Embedder.Embed` takes a purpose, `Document` or `Query`, so providers can treat stored content and queries
  differently.
- The Ollama client prepends `embeddings.ollama.document_prefix` or `query_prefix` according to the purpose. The
  default `auto` resolves from a table of known models, keyed by base model name, and resolves to no prefix for
  unknown models. Any other value is used literally.
- The model identity recorded with each stored vector includes the document prefix, for example
  `ollama/nomic-embed-text document_prefix="search_document: "`. The query prefix is not part of the identity,
  because it never affects stored vectors.

## Consequences

- Retrieval quality improves for `nomic-embed-text` without any configuration.
- Changing the model or the document prefix makes existing vectors stale through the existing mechanism. Search
  degrades to keywords with a warning until `ralph index` re-embeds them; nothing is silently mixed.
- Upgrading from a build without prefixes requires one `ralph index` run.
- Supporting another asymmetric model means adding its prefixes to the known-model table.
