# 4. Pure-Go vector search with an FTS5 keyword fallback

- Status: Accepted
- Date: 2026-11-06

## Context

Documents, milestones, and tasks must be searchable by meaning. Options included the `sqlite-vec` extension (native
k-nearest-neighbor search, but it needs cgo and a C toolchain), an embedded vector database, or brute force over
vectors stored in SQLite. A project's plan holds at most thousands of chunks.

## Decision

- Store embeddings as little-endian float32 BLOBs in a `chunks` table, and rank them in Go by cosine similarity,
  keeping each entity's best chunk.
- Mirror chunk text into an FTS5 table, and use `bm25` keyword search whenever vectors are unavailable: no embedded
  content yet, or the query cannot be embedded.
- Keep this behind the `store` and `search` interfaces so an approximate-nearest-neighbor index can replace it later.

## Consequences

- No cgo; search is exact. Cost grows linearly with corpus size, which is fine at the expected scale.
- Search always returns something, and reports which mode ran.
- Vectors from different models are never compared; changing the model makes chunks stale until `ralph index` runs.
