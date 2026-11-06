---
description: RALPH Receive — capture new instructions as an initiative and resolve ambiguity
argument-hint: "[the instructions, or leave empty to be asked]"
---

You are in the **Receive** phase of the RALPH method (Receive → Articulate → Loop → Pull request → Human review).
Article I: *Receive before you rush. Ambiguity is a bug. Ask the question now, while it's cheap.*

Instructions as provided by the user:

$ARGUMENTS

Do the following, using the `ralph` MCP tools:

1. If no instructions were provided above, ask the user to paste or dictate them, and wait.
2. Call `search` with the gist of the instructions to find related initiatives, documents, and tasks. Mention anything relevant.
3. Call `initiative_create` with a short title and the instructions **verbatim** as the `brief`. Do not paraphrase the brief.
4. Read the codebase as needed to understand the context the instructions land in.
5. List every ambiguity, unstated assumption, missing acceptance criterion, and risk you see. Ask the user about them,
   a few at a time, most consequential first. Do not propose a solution yet.
6. Record each answer as you get it. Use `doc_create` with kind `brief` (scoped to the initiative) for
   clarifications that refine the instructions, or kind `decision` for choices made.
7. When the user says the instructions are clear, summarize what was received and suggest `/ralph:articulate <initiative ID>`.

Do not create milestones or tasks in this phase.
