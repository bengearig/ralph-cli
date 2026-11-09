# 16. Record the loop's git state

- Status: Accepted
- Date: 2026-11-09
- Refines: [13. Loop execution policy](0013-loop-execution-policy.md)

## Context

`ralph loop` commits each `DONE` task on the milestone branch and prints the hash, but nothing was kept. The branch
name was rederived from the current `loop.branch_template` every time it was needed, so changing the template, or a
milestone title it uses, pointed the loop and `ralph pr create` at a branch that held none of the work. Once a person
added commits to the branch, rebased, amended, squashed, or reset it, ralph had no way to tell which commits belonged
to which task, or whether the branch still matched what the loop had left, and a pull request could be opened for
content that no longer matched the milestone's `DONE` tasks. Detecting that drift needs a record of what the loop
did to compare against.

## Decision

- **What is recorded.** A milestone records its branch, the base commit the branch started from, and its head
  commit. A task records the commit the loop made when it reached `DONE`. Empty means not recorded.
- **Full hashes.** Commits are stored as full object names, never abbreviated: an abbreviation can become ambiguous
  as the repository grows, while a full name stays exact, in SHA-1 and SHA-256 repositories alike. Output for people
  shortens them; JSON and MCP results carry them in full.
- **Dedicated setters.** The records are written only through their own store and service methods. `UpdateMilestone`
  and `UpdateTask` never write them, and no CLI flag or MCP tool sets them. An edit through the CLI or MCP, possibly
  made by another process while the loop runs, therefore cannot overwrite a recorded head, and the records only ever
  describe what the loop saw. Writing them does not change `updated_at`, which stays a record of plan edits.
- **Trust on first use.** When the loop finds that the branch already exists and the milestone has no record, it
  accepts the branch as it is: it records the current tip as the head and the merge base with `pr.base_branch` as
  the base. Milestones looped before records existed then start being recorded without a separate migration step.
  Tasks finished before that point keep an empty commit, to be mapped from history later.
- **The recorded branch name wins.** Once recorded, the branch name is used instead of `loop.branch_template` by
  both the loop and `ralph pr create`, through one shared function, so the two cannot disagree. Changing the
  template, or a milestone title it uses, no longer abandons work in progress. If the recorded branch no longer
  exists, the loop warns, falls back to the template, and records the result afresh.
- **The head is recorded after every iteration**, whatever its outcome, and also when a session error, a commit
  failure, or an interruption ends the run mid-iteration. The head then follows commits an agent made itself and
  runs with `loop.commit_per_task: false`, not only ralph's own commits. A head recorded only on success would leave
  the branch looking drifted after every failed or interrupted iteration.
- **A `Ralph-Task: T-n` trailer on loop commits**, controlled by `loop.commit_trailer`, which defaults to `true`. The
  trailer is added by the loop with `git commit --trailer` (git 2.32 or newer), independently of
  `loop.commit_template`, so a custom template cannot drop it and it joins any trailer block the template produces.
  Unlike a hash, a trailer survives a rebase, an amend, and a cherry-pick, and a squash whose message keeps
  trailers, so commits can still be mapped to tasks after history is rewritten. The setting exists for teams that do
  not want ralph's IDs in their history; commits can then be matched only by subject and content.
- **Records live in the per-clone database.** They are stored with the rest of the plan in the git-ignored
  `.ralph/ralph.db` ([ADR 3](0003-sqlite-store-in-repo-gitignored.md)). Branches and their commits are themselves a
  property of one clone until pushed, and the task IDs the records refer to are per database
  ([ADR 11](0011-display-ids.md)). Keeping them in the repository instead, for example in git notes or a committed
  file, would add commits or refs to the branch under review and share IDs that mean nothing in another clone.

## Consequences

- `ralph milestone show`, `ralph task show`, their `--json` forms, and the MCP `milestone_get` and `task_get` results
  show the recorded state. The records are otherwise purely additive: nothing yet acts on them beyond branch
  resolution.
- Detecting drift between the record and the branch, and reconciling it, are a follow-up. They will compare the
  recorded head with the branch tip, map rewritten commits back to tasks through the trailers, and replace the
  fallback for a missing recorded branch.
- Like the rest of the plan, the records are not shared between clones; a clone that did not run the loop starts
  unrecorded and records on first use.
- `Ralph-Task` trailers name per-clone task IDs, so they are meaningful only alongside the database that issued them.
- `loop.commit_trailer: true` requires git 2.32 or newer.
