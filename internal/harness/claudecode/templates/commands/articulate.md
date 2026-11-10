---
description: RALPH Articulate — work out each task's details with the user and promote it to TODO
argument-hint: "<initiative, milestone, or task ID, e.g. I-1, M-2, T-3>"
---

You are in the **Articulate** phase of the RALPH method (Receive → Articulate → Loop → Pull request → Human review).
Article II: *Articulate before you act. Write the solution down in words a stranger could follow. If you can't
explain it, you can't loop it.*

Articulate plans at a **low level**: the exact details of each task. The high-level plan (milestones and their
unplanned tasks) came from `/ralph:receive`.

Scope: $ARGUMENTS

Work with the user, using the `ralph` MCP tools:

1. Resolve the scope: an initiative (`initiative_get`), a milestone (`milestone_get`), or a single task (`task_get`).
   If no ID was given, call `status` and ask which to articulate.
2. Load context: the initiative's brief, its documents and the project-wide ones (`doc_list` for the initiative and
   with `project_only`, then `doc_get` for anything relevant), and `search` for prior decisions and related work.
3. Take the `PROPOSED` tasks in scope one at a time, in milestone order and then position order. For each task:
   - read the code it will touch;
   - ask the user about the low-level details, a few questions at a time: exact behavior, interfaces and data, edge
     cases and error handling, and which tests or commands will prove it works;
   - store decisions that matter beyond this one task as `design` or `decision` documents;
   - write the result with `task_update`: a description that an agent with **no access to this conversation** could
     implement from, and acceptance criteria that are verifiable, preferably by tests or commands;
   - as soon as both are settled, promote the task with `task_set_status` to `TODO`, giving a short reason. Do not
     ask for separate permission to promote.
4. Within a milestone, and with the user's agreement, you may add tasks (`task_create`), split or merge them, reorder
   them (`position`), or cancel them (`task_set_status` `CANCELLED` with a reason). Cancel by default: delete a task (`task_delete`) only if it was created by mistake, and only after asking the user. Each task must be small enough to
   finish in one loop iteration and must never depend on a later one.
5. If a milestone needs to change (added, removed, re-scoped, or reordered), or the instructions themselves turn out
   to be unclear, stop. Add a `KICKBACK` note (`task_add_note`) to the affected task when there is one, explain the
   problem, and suggest `/ralph:receive <initiative ID>` (the Kickback Clause) rather than guessing.
6. Leave `TODO` tasks alone unless the user wants to revisit one; move it back to `PROPOSED` first.
7. Finish with `status` and summarize: which tasks were promoted, which remain `PROPOSED` and why, and
   `ralph loop --milestone <ID>` for each milestone that is now `READY`.

Do not write production code in this phase.
