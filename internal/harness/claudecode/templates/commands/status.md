---
description: RALPH status — summarize initiatives, milestones, and task progress
argument-hint: "[initiative ID]"
---

Summarize RALPH progress using the `ralph` MCP tools.

Initiative filter (may be empty): $ARGUMENTS

1. Call `status` (with `initiative_id` if one was given above).
2. For each milestone, report its status and task counts. Call out:
   - `BLOCKED` tasks, with their reasons (`task_list` with status `BLOCKED`, then `task_get`);
   - `PROPOSED` tasks that still need articulation, suggesting `/ralph:articulate <milestone ID>`;
   - milestones whose tasks are all `TODO`, which are ready for `ralph loop --milestone <ID>`.
3. Suggest the single most useful next step.

Keep the summary short. Do not change anything.
