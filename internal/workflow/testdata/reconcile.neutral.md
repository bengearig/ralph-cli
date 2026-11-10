Reconcile a milestone's branch drift using the `ralph` MCP tools. **Drift** means the milestone's branch no longer
matches what ralph recorded (its branch, base, head, and each `DONE` task's commit), typically after a rebase, squash,
amend, reset, or rename. `ralph pr create` refuses a drifted milestone, and so does `ralph loop` in most states.

Milestone: (none given)

1. If no milestone ID was given above, call `status` and ask the user which milestone to reconcile, and wait.
2. Gather the facts with read-only tools, which work inside or outside plan mode: `milestone_get` for the milestone
   and its tasks, and `milestone_drift` for the drift report.
3. If the report's `state` is `unchanged`, say that the branch is exactly as recorded and nothing needs reconciling,
   suggest `ralph pr create --milestone <ID>`, and stop. Change nothing.
4. Otherwise explain the drift in plain language, starting with the `state`:
   - `ahead`: commits were added on top of the recorded head (`extra_commits`). Recorded work is intact, but nothing
     accounts for the extra commits yet.
   - `behind`: the branch was moved back, and recorded commits are no longer on it (`lost_commits`).
   - `diverged`: history was rewritten (rebase, amend, squash, or reset onto other work), so the recorded head is not
     on the branch.
   - `unrecorded`: the milestone has no recorded head, for example because it was looped before records existed. The
     report rebuilds what it can, and `proposed_base` is the base that accepting would record.
   - `branch_missing`: the recorded branch does not exist locally. Say whether `origin/<branch>` exists
     (`remote_branch_exists`). Ask whether the branch was renamed, and if so, ask the user for its new name.
   - `merged`: the branch is already merged into the base branch, so there is nothing to open a pull request for. The
     branch may already be deleted (`branch_exists` is false), which is fine. `merged_by` says how it was found:
     `ancestry` means the milestone's head is in the base branch's history; `tasks` means each task's work was found
     in the base branch (a squash or rebase merge), and the task findings point at those base-branch commits.

   Then go through each entry in `tasks` and say where that `DONE` task's work is now, by its `match`:
   - `present`: its recorded commit is still on the branch.
   - `trailer`: a commit on the branch carries its `Ralph-Task: <task ID>` trailer, so the work survived a rewrite.
   - `patch_id`: a commit on the branch makes the same change as the recorded commit, on a different parent.
   - `subject`: a commit on the branch has the subject the loop would give this task's commit.
   - `missing`: nothing on the branch matched, so the task's work may be gone.

   Finally, list any `extra_commits`, `lost_commits`, and `unattributed_commits` (commits on the branch that no task
   claims, such as work added by hand) with their hashes and subjects, and say what each list means for the milestone.
5. Propose actions, and explain each recommendation:
   - `branch` with the new name, when the branch was renamed;
   - `accept`, to record the branch as it is now, when history was rewritten or added to and every task matched;
   - for each `missing` task, either reopen it, with a reason, so the loop redoes it, or keep it `DONE`, for example
     when its work was folded into another commit or deliberately dropped. Recommend one and say why. Accepting
     without reopening keeps the task `DONE` with its commit cleared and a note that no commit was found.

   `accept` is refused for `merged`, and for `branch_missing` unless `branch` points the record at an existing branch.
   If no action can resolve the drift, say so, propose nothing, and stop.
6. Get the user's approval of the proposed actions: the branch, the tasks to reopen with the reason, and whether to
   accept, together with the equivalent command, for example
   `ralph milestone reconcile <ID> --branch <name> --reopen T-3,T-4 --reason "<reason>" --accept`.
   Get approval in the first of these ways that the harness supports: through its plan or approval mode, presenting the proposed actions as the plan (if the user rejects it with feedback, revise and present it again); otherwise through its structured question tool, showing the proposed actions and asking for an explicit yes or no; otherwise by presenting the proposed actions in a message and waiting for the user to explicitly confirm. In every case, apply nothing until it is approved. If the user declines, apply nothing: print the equivalent CLI command
   for the user to run.
7. After the plan is approved, call `milestone_reconcile` with exactly the approved actions (`branch`, `reopen`,
   `reason`, `accept`), and nothing else. If it returns an error, nothing was changed: explain the error, revise the
   proposal, and get approval again as in step 6. Otherwise summarize the `after` report: its state and each task's
   finding.
8. Suggest the next step: `ralph pr create --milestone <ID>` if the `after` state is `unchanged`, or
   `ralph loop --milestone <ID>` if tasks were reopened. If the milestone is still not clean, explain what remains.

Never change anything without the user's approval, never edit tasks beyond reopening them through
`milestone_reconcile`, and never run git commands to "fix" the branch.
