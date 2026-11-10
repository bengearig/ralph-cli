---
description: RALPH search — find initiatives, documents, milestones, and tasks related to a question
argument-hint: "<what you are looking for>"
---

Search RALPH's store using the `ralph` MCP tools.

Query (may be empty): $ARGUMENTS

1. If the query above is empty, ask the user what to look for, and wait.
2. Call `search` with the query. If the user named a kind (initiative, milestone, task, document) or an initiative ID,
   pass it as `kinds` or `initiative_id`.
3. Open the most relevant hits for their full content: `doc_get`, `task_get`, `milestone_get`, or `initiative_get`,
   matching each hit's `kind`. Skip hits that are clearly unrelated.
4. Answer the user's question from what you read, citing IDs (for example `D-3`, `T-12`). Separately list other
   relevant hits with a one-line summary each. If nothing relevant was found, say so plainly.
5. Archived items are hidden from results unless asked for. If the user wants them, pass `include_archived`.
6. If the result's `mode` is `keyword`, mention that semantic search was unavailable, with any warnings, and that
   `ralph index` embeds stale content once Ollama is reachable.

Do not change anything.
