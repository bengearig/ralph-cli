---
description: RALPH Articulate — design the solution and break it into loopable tasks
argument-hint: "<initiative ID, e.g. I-1>"
---

You are in the **Articulate** phase of the RALPH method (Receive → Articulate → Loop → Pull request → Human review).
Article II: *Articulate before you act. Write the solution down in words a stranger could follow. If you can't
explain it, you can't loop it.*

Initiative: $ARGUMENTS

Work with the user, using the `ralph` MCP tools:

1. Load context: `initiative_get`, `doc_list` for the initiative and for the project (`project_only`), and `doc_get`
   for anything relevant. Use `search` to find prior decisions and related work. If no initiative ID was given,
   call `initiative_list` and ask which one.
2. Draft or update the architecture and design. Store them with `doc_create` / `doc_update` (kinds `architecture`,
   `design`, `decision`). Project-wide architecture belongs at project scope; initiative-specific design belongs to the
   initiative. Review each with the user before moving on.
3. Split the work into milestones with `milestone_create`. Each milestone becomes one branch and one pull request,
   so each must be independently reviewable and leave the codebase working.
4. Propose tasks with `task_create`. Tasks start `PROPOSED`. A good task:
   - is small enough to finish in one loop iteration;
   - has a description that an agent with **no access to this conversation** could implement from;
   - has acceptance criteria that are verifiable, preferably by tests or commands;
   - is positioned so that earlier tasks never depend on later ones.
5. Refine each task with the user (`task_update`). When a task is fully articulated, promote it with
   `task_set_status` to `TODO`, giving a short reason. Leave tasks that still have open questions as `PROPOSED`.
6. Finish with `status` and summarize: documents written, milestones, and which tasks are `TODO` versus still `PROPOSED`.

If articulation reveals that the instructions themselves are unclear, say so and return to `/ralph:receive`
(the Kickback Clause) rather than guessing.

Do not write production code in this phase.
