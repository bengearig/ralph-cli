# Tracker fixtures

Dissimilar sample trackers used to check that the import workflow recognises a task list by its content and not by its
name, format or originating tool. They are deliberately varied: checklist, table, JSON, CSV and free-form log.

| Fixture | Form |
|---------|------|
| `checklist.md` | checkbox list with milestone headings and per-item description and acceptance |
| `table.md` | markdown table with a status column and an ungrouped item |
| `issues.json` | JSON issue export with labels, states and a blocker |
| `backlog.csv` | CSV records grouped by a sprint column |
| `progress.log` | free-form progress log |

## Manual check before merging a workflow change

The check is interactive, so loop agents can't run it. List it as a human-review step in the pull request.

For each fixture, in a scratch project:

1. Queue it: `ralph import --file internal/workflow/testdata/trackers/<fixture>`.
2. Run `/ralph:import` in the agent session.
3. Confirm the result:
   - one new initiative whose brief starts with `Imported from <path>.` followed by any goal text from the fixture;
   - milestones follow the fixture's grouping and order, and ungrouped items land in a milestone the agent named;
   - each task's status matches its marker: done as `DONE`, dropped as `CANCELLED`, blocked as `BLOCKED` with the
     fixture's blocker as the reason, `TODO` only with both a description and acceptance criteria, and everything
     else, including in-progress and unclear items, as `PROPOSED`, never `IN_PROGRESS`;
   - every task has a reason explaining the mapping, and `source` is the fixture path;
   - the import record is `done` with the note `imported as I-n`;
   - with `confirm_outline` on, or when you ask to approve first, nothing is created until the proposal is approved.
