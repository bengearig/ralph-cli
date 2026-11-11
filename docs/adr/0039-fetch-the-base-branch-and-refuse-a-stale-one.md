# 39. Fetch the base branch and refuse a stale one

- Status: Accepted
- Date: 2026-11-11

## Context

After a pull request is merged on the host, the local base branch is stale until someone pulls. A new milestone branch is
then created from old code, and the dependency check, which reads the local base branch, reports a merged dependency as
unmerged and suggests `ralph milestone merge`, which is the wrong fix.

Until now ralph never fetched. `ralph pr create` was the one command that talked to a remote, and only to push (see
[ADR 9](0009-branch-and-pull-request-per-milestone.md)). Knowing that the base is stale needs the remote's current
state, so that rule has to change.

## Decision

- **Fetching is now allowed, in two commands.** `ralph loop` and `ralph pr create` fetch `pr.remote`'s copy of
  `pr.base_branch` before they start. This replaces the earlier rule that `ralph pr create` is the one command that
  talks to a remote.
- **Some commands never fetch.** `ralph milestone merge`, the read-only views, and the dependency check do not talk to
  a remote. The views and the check read the remote-tracking ref as the last fetch left it.
- **Only the base branch is fetched**, with an explicit refspec into `refs/remotes/<pr.remote>/<pr.base_branch>`. No
  other remote-tracking branch and no tag changes, and a fetch leaves the local branch, HEAD, index, and work tree
  alone. It runs with `GIT_TERMINAL_PROMPT=0`, so a remote that wants credentials fails instead of prompting.
- **The default is to refuse.** When the local base branch is behind the remote's, or has diverged from it, the command
  stops with exit code `3` and names the fix. A base that is current, or ahead, passes.
- **A failed fetch refuses.** A missing remote, an unreachable one, or a remote without the base branch is a failed
  fetch, and an unknown remote state is treated as unsafe.
- **`pr.stale_base: sync` fast-forwards instead.** When the base is behind, ralph fast-forwards the local base branch
  itself, only if it can be done as a fast-forward and the work tree is clean. Otherwise, and for a diverged base, the
  command refuses as under the default.
- **`--force` skips the guard**, and so the fetch, as it skips the dependency check.
- **`--dry-run` fetches, so its report is accurate, and only warns.** A stale base or a failed fetch is reported as a
  warning rather than a refusal, and a dry run never fast-forwards, even under `sync`.
- **The guard runs first.** It comes before the dependency check and before any branch is created.
- **A missing local base branch is not staleness.** The loop already falls back to the remote-tracking ref.
- **`ralph sync` does the fast-forward on demand.** It fetches the base branch, fast-forwards the local one, and can
  remove the local branches of the milestones it merged, with git's safe delete only. The guard's refusal messages and
  the `base_stale` fix name it.
- **The dependency check gets a state for it.** A dependency merged into the remote's base but not yet into the local
  one is `base_stale`, not unmerged, and its fix is to update the base branch.

### Alternatives not chosen

- Fetching in every command that reads the base. Read-only views would then need the network and could fail without it.
- Syncing silently by default. Moving a branch the person has checked out is their decision, so it is opt-in.
- Fetching everything from the remote. It changes more remote-tracking refs and tags than the guard needs.
- Treating a failed fetch as a warning. The check would then pass exactly when it knows least.

## Consequences

- A stale base is caught before a milestone branch is cut from it, and a merged dependency is no longer reported as
  unmerged.
- `ralph loop` and `ralph pr create` need a reachable `pr.remote` with the base branch. A repository without one needs
  `--force`.
- The documentation can no longer say that only `ralph pr create` talks to a remote; it says which commands fetch and
  which never do.
- Under `sync` a command can move the local base branch, but only by a fast-forward on a clean work tree, never with a
  merge commit or a rewrite.
