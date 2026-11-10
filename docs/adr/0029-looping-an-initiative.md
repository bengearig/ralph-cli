# 29. Looping an initiative

- Status: Accepted
- Date: 2026-11-10
- Refines: [9. Branch and pull request per milestone](0009-branch-and-pull-request-per-milestone.md), [27. Merge milestones locally with fast-forward only](0027-merge-milestones-locally-with-fast-forward-only.md), [28. Milestone dependencies](0028-milestone-dependencies.md)

## Context

Until now the loop was started for a single named milestone, and each milestone becomes its own branch and pull
request. An initiative is several milestones, often in a sequence where each builds on the one before, so finishing an
initiative meant a person
running the loop, reviewing, merging, and running it again, once per milestone, and remembering which one was next.
With milestone merging and dependencies in place, ralph has what it needs to do the sequencing, but the rule that
agents propose and people merge still has to hold.

## Decision

- **A command that takes an initiative.** `ralph loop --initiative I-n` replaces `--milestone M-n`; exactly one of the
  two is required. It classifies the initiative's milestones in position order and acts on the first one that is
  neither merged nor cancelled. Merged and cancelled milestones are skipped because there is nothing left to do for
  them. A milestone with loopable tasks and satisfied dependencies is run exactly as `--milestone` would run it. A
  finished milestone that is not yet merged needs a merge. Any other milestone, whether it is waiting on a dependency,
  has blocked tasks, or has no tasks ready, stops the chain with a reason and the command that fixes it, and exit
  code 3. The order is the milestones' position order and nothing is reordered to get around a stop: a milestone
  later in the order does not run while an earlier one needs a person.
- **One milestone per invocation by default.** Without further flags the loop runs the first runnable milestone to its
  end and stops, so a person can review it and merge it before the next one starts. Running the command again finds
  the finished milestone waiting for a merge and says so until it is merged.
- **Merging is opt-in with `--merge`.** With `--merge` the loop fast-forwards each finished milestone into the base
  branch with the same operation as `ralph milestone merge` and carries on to the next, until the initiative is
  complete or something stops it. This keeps "agents propose, people merge" intact: the person who passes `--merge` is
  the one who decided to merge, and does so knowing the loop will do it for them. `--merge` without `--initiative` is
  an error.
- **Safety rules.** A milestone that is not `DONE` is never merged: if a run ends with the milestone unfinished, the
  chain stops. A refused merge, whether from drift, a dirty working tree, or a base branch that cannot be
  fast-forwarded, stops the chain and prints the refusal. Merges are fast-forward only and never create a merge
  commit. `--force` skips the dependency check and nothing else; it never overrides a merge refusal.
- **State is re-read before every step.** After each run or merge the chain classifies the milestones again from the
  store and the repository instead of trusting what it believed earlier, so a change made while it ran is seen.
- **A progress guard.** The chain takes a bounded number of steps, proportional to the number of milestones, since each
  milestone needs at most one run and one merge. If it takes more than that, it stops with an error that names
  `--dry-run`, rather than looping on a state it cannot advance.
- **The branch at the end.** Each run returns to the branch it started from, as in
  [ADR 27](0027-merge-milestones-locally-with-fast-forward-only.md), so without `--merge` the starting branch is
  checked out at the end. A merge leaves the base branch checked out, and so does a chain that merged.
- **Exit codes follow the existing convention.** `0` when the milestone that was run ended `DONE` or the initiative is
  complete; `3` when a person is needed; `1` for an error or an interruption. `--dry-run` prints the ordered milestones,
  their dispositions, and the next step, and changes nothing.
- **No MCP tool.** As with merging, running a chain is a command a person starts.

## Alternatives considered

- **Merge by default.** Rejected: it makes every initiative run merge into the base branch without anyone having
  asked for it, which is the step people want to keep. A person who wants the whole chain says so with `--merge`.
- **Always stop for a human merge.** Rejected as the only mode: for initiatives whose milestones are reviewed together,
  or whose repository has no hosting service, running the loop and the merge by hand once per milestone is
  bookkeeping with no review in it. The default still stops, so this is the behavior unless `--merge` is passed.
- **Stacked branches.** Branching each milestone from the previous one's unmerged branch would let the whole initiative
  run without merging anything, but it needs rebase handling when an earlier milestone changes in review, and a story
  for stacked pull requests on each provider. It is not part of this decision, and
  [ADR 28](0028-milestone-dependencies.md) leaves it out as well.
- **Skipping past a stopped milestone.** Rejected: later milestones usually build on earlier ones, and an agent that
  ran ahead would be writing against code that is not in the base branch. Dependencies are the way to say that a
  milestone is independent of an earlier stop; the order is not otherwise bent.

## Consequences

- An initiative can be finished with one command and one decision, `--merge`, or reviewed milestone by milestone with
  the same command run repeatedly.
- A chain that stops leaves the repository in a state a person can read: finished milestones merged, the one that
  stopped with its reason and the fix printed, and nothing after it started.
- The chain classifies every milestone again before each step, which costs a drift check per finished milestone.
- Merging with `--merge` changes the base branch of the local repository only. Nothing is pushed.
