---
description: RALPH Receive — capture instructions and plan them at a high level: initiative, milestones, unplanned tasks
argument-hint: "[the instructions, or an initiative ID such as I-1 to revise its plan]"
---

You are in the **Receive** phase of the RALPH method (Receive → Articulate → Loop → Pull request → Human review).
Article I: *Receive before you rush. Ambiguity is a bug. Ask questions now, while it's cheap.*

Receive plans at a **high level**: what is being asked, why, and how it breaks into milestones and unplanned tasks.
The low-level details of each task are worked out later, in `/ralph:articulate`.

Arguments:

$ARGUMENTS

Do the following, using the `ralph` MCP tools:

1. If the arguments above are empty, ask the user to paste or dictate the instructions, and wait. If they are only an
   initiative ID (such as `I-1`), follow **Revising an initiative** below instead. Otherwise they are new instructions.
2. Call `search` with the gist of the instructions to find related initiatives, documents, and tasks. Mention anything
   relevant.
3. Call `initiative_create` with a short title and the instructions **verbatim** as the `brief`. Do not paraphrase the
   brief.
4. Read the codebase only as far as needed to understand where the work lands.
5. Ask the user high-level questions, a few at a time, most consequential first: goals and what success looks like,
   scope and non-goals, who is affected, constraints, integrations, risks, and the order in which value should be
   delivered. Leave implementation details for Articulate.
6. Record each answer as you get it, with `doc_create` scoped to the initiative: kind `brief` for clarifications of
   the instructions, `decision` for choices made. Write an `architecture` document (project-wide or for the
   initiative) only for the high-level structure that the milestone split depends on.
7. Propose an outline and revise it with the user until they agree:
   - ordered milestones, each with a one-line description. Each milestone becomes one branch and one pull request, so
     each must be independently reviewable and leave the codebase working;
   - under each milestone, its **unplanned tasks**: an imperative title and, optionally, a one-sentence summary.
8. Only once the user agrees, create the outline: `milestone_create` for each milestone in order, then `task_create`
   for each task with its title and summary as the description. Do not write acceptance criteria. The tasks stay
   `PROPOSED` until Articulate.
9. Finish with `status`, summarize what was received and planned, and suggest `/ralph:articulate <initiative ID>`
   (or a milestone ID to start with one milestone).

## Revising an initiative

Use this when the arguments are an initiative ID, typically after a kickback from Articulate or the loop.

1. Load the current plan: `initiative_get`, `milestone_list` and `task_list` for the initiative, and `doc_list` for
   the initiative. Read the `KICKBACK` notes (`task_get`) to learn what prompted the revision.
2. Discuss the high-level change with the user, as in step 5 above. Record it as `brief` or `decision` documents; the
   initiative's brief stays verbatim.
3. Propose the revised outline and, once the user agrees, apply it:
   - new milestones with `milestone_create`, changed ones with `milestone_update`;
   - new unplanned tasks with `task_create`;
   - drop a task, or a whole milestone's tasks, with `task_set_status` `CANCELLED` and a reason (nothing is deleted);
   - move `TODO` tasks whose scope changed back to `PROPOSED`, with a reason, so they are articulated again.
4. Finish as in step 9 above.

Do not work out task details, write acceptance criteria, promote tasks to `TODO`, or write code in this phase.
