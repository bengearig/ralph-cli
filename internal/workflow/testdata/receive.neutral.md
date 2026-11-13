You are in the **Receive** phase of the RALPH method (Receive → Articulate → Loop → Pull request → Human review).
Article I: *Receive before you rush. Ambiguity is a bug. Ask questions now, while it's cheap.*

Receive plans at a **high level**: what is being asked, why, and how it breaks into milestones and unplanned tasks.
The low-level details of each task are worked out later, in the `articulate` workflow (MCP prompt `articulate`).

Arguments:

(none given)

Do the following, using the `ralph` MCP tools:

1. If the arguments above are empty, ask the user to paste or dictate the instructions, and wait. If they are only an
   initiative ID (such as `I-1`), follow **Revising an initiative** below instead. Otherwise they are new instructions.
   If they are new instructions and the harness has a plan mode and it is already active, follow **Starting in plan mode** below.
2. Call `search` with the gist of the instructions to find related initiatives, documents, and tasks. Mention anything
   relevant.
3. Call `initiative_create` with a short title and the instructions **verbatim** as the `brief`. Do not paraphrase the
   brief; it keeps any mention of the parent card and the sprint. When the user names a parent Jira card for the
   initiative (for example "under PROJ-123" or "attach it to ABC-9"), pass its key as `parent_card`; otherwise leave
   `parent_card` out. Never guess a key. When the user names a Jira sprint for the initiative's cards (for example "put
   it in Sprint 42" or "in the current sprint's successor, Sprint 43"), pass the name as `sprint`; otherwise leave
   `sprint` out, which means the board's active sprint. Never guess a sprint name. Note `confirm_outline` in the
   result; step 8 uses it.
4. Read the codebase only as far as needed to understand where the work lands.
5. Ask the user high-level questions, a few at a time, most consequential first: goals and what success looks like,
   scope and non-goals, who is affected, constraints, integrations, risks, and the order in which value should be
   delivered. Leave implementation details for Articulate.
6. Record each answer as you get it, with `doc_create` scoped to the initiative: kind `brief` for clarifications of
   the instructions, `decision` for choices made. Write an `architecture` document (project-wide or for the
   initiative) only for the high-level structure that the milestone split depends on.
7. Draw up an outline:
   - ordered milestones, each with a one-line description. Each milestone becomes one branch and one pull request, so
     each must be independently reviewable and leave the codebase working;
   - under each milestone, its **unplanned tasks**: an imperative title and, optionally, a one-sentence summary;
   - for each milestone, the milestones it **depends on**: those whose code must be merged into the base branch before
     it can start, in this initiative or another. Record them with `depends_on` (on `milestone_create`, or later on
     `milestone_update`) instead of leaving "builds on X" only in prose. Order alone is not a dependency.
8. Create the outline, first getting it approved if approval is needed. Approval is needed if `confirm_outline` from
   step 3 is true, or if the user asked to approve changes before they are made: in the arguments, in the
   conversation, or in project instructions such as CLAUDE.md. Otherwise it is not needed.
   - **No approval needed:** create the outline right away: `milestone_create` for each milestone in order (with
     `depends_on` when it needs another milestone's code merged first, see below), then
     `task_create` for each task with its title and summary as the description. Do not write acceptance criteria. The
     tasks stay `PROPOSED` until Articulate. Then show the user the outline with the new IDs (milestones with their
     one-line descriptions, tasks under each) and invite changes. Do not end by asking for agreement.
   - **Approval needed:** follow **Approving the outline** below. Once the plan is approved, create the outline
     and show it with its IDs as above.
   - If the user asks for changes to the outline just created, revise it in place: `milestone_update`
     and `task_update` for edits, `milestone_create` and `task_create` for additions, and `task_set_status`
     `CANCELLED` with a reason for tasks no longer wanted. Record any new decisions as documents, as in step 6.
9. Finish with `status`, summarize what was received and planned, and suggest the `articulate` workflow (MCP prompt `articulate`) for <initiative ID>
   (or a milestone ID to start with one milestone).

## Approving the outline

Whenever approval is needed, create or apply the outline only once it is approved.

Get approval in the first of these ways that the harness supports: through its plan or approval mode, presenting the outline as the plan (if the user rejects it with feedback, revise and present it again); otherwise through its structured question tool, showing the outline and asking for an explicit yes or no; otherwise by presenting the outline in a message and waiting for the user to explicitly confirm. In every case, apply nothing until it is approved. If the user declines, create nothing.

Write the outline as the plan or message: milestones in order with their one-line descriptions, and tasks under each.

## Starting in plan mode

Skip this section unless the harness has a plan mode and it is already active as Receive starts. Write tools are
unavailable in plan mode until the plan is approved.

1. Do the read-only work of steps 1, 2, 4, and 5 above: search, read the code, and ask the high-level questions. Do
   not call `initiative_create` or `doc_create` yet.
2. Write the plan: the initiative title, the instructions **verbatim** as the brief, the `brief`, `decision`, and any
   `architecture` documents to record (as in step 6), the parent card and the sprint when the user named them, and the outline
   (as in step 7).
3. Request approval through the plan mode's approval step. If the user rejects the plan with feedback, revise it and
   present it again.
4. Once the plan is approved, call `initiative_create` (passing the same `parent_card` and `sprint`, if any), then
   `doc_create` for each document, then create the outline as in step 8 and show it with its IDs. Approval has already
   been given, so `confirm_outline` in the `initiative_create` result changes nothing.
5. Finish as in step 9 above.

## Revising an initiative

Use this when the arguments are an initiative ID, typically after a kickback from Articulate or the loop.

1. Load the current plan: `initiative_get`, `milestone_list` and `task_list` for the initiative, and `doc_list` for
   the initiative. Read the `KICKBACK` notes (`task_get`) to learn what prompted the revision.
2. Discuss the high-level change with the user, as in step 5 above. Record it as `brief` or `decision` documents; the
   initiative's brief stays verbatim. When the user gives, changes, or removes the parent card, call
   `initiative_update` with `parent_card` (an empty string removes it). When the user gives, changes, or removes the
   sprint, call `initiative_update` with `sprint` (an empty string removes it).
3. Revisions always need approval. Get the revised outline approved, as in **Approving the outline** above, and
   once it is approved, apply it:
   - new milestones with `milestone_create`, changed ones with `milestone_update`, setting `depends_on` for any
     milestone that needs another milestone's code merged first (the list replaces the existing one);
   - new unplanned tasks with `task_create`;
   - drop a task, or a whole milestone's tasks, with `task_set_status` `CANCELLED` and a reason. Cancel by default: delete (`task_delete`, `milestone_delete`) only items created by mistake, such as duplicates, and only after asking the user, never with `force` unless they explicitly approve;
   - move `TODO` tasks whose scope changed back to `PROPOSED`, with a reason, so they are articulated again.
4. Finish as in step 9 above.

Do not work out task details, write acceptance criteria, promote tasks to `TODO`, or write code in this phase.
