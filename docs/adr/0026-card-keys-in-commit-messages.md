# 26. Card keys in commit messages

- Status: Accepted
- Date: 2026-11-10
- Refines: [16. Record the loop's git state](0016-record-the-loops-git-state.md), [23. Card keys in branch names](0023-card-keys-in-branch-names.md)

## Context

Teams that track work in Jira want commits to be findable from the card: the key in the subject, or somewhere a tool
can read it. The loop commits per task, so the card that matters is the task's own. The drift check
matches commits to tasks by the `Ralph-Task` trailer, then by patch id, then by the rendered subject
([ADR 17](0017-detect-and-reconcile-branch-drift.md)), so a key that appears in the subject after commits were made
changes what the subject matcher expects.

## Decision

A task with no card of its own uses its milestone's card key; see [ADR 45](0045-choose-which-kinds-get-jira-cards.md).

- **Opt-in template fields on the task.** `.Task` in `loop.commit_template` gains `.CardKey`, the key of the task's own
  card or empty, and `.CardKeyPrefix`. The default template does not use them, so no commit message changes until a team
  edits the template. Initiative keys are not added to commits.
- **Bracketed prefix.** `.CardKeyPrefix` is the key in square brackets followed by a space, `[ABC-1235] `, or empty, so
  `{{.Task.CardKeyPrefix}}{{.Task.ID}}: {{.Task.Title}}` gives `[ABC-1235] T-12: Add the Jira client` or, without a
  card, `T-12: Add the Jira client`.
- **Clean fallback.** When the template uses a key field and the task has no card, the first line is tidied: empty
  bracket and parenthesis pairs go, spaces collapse, and leading separators are trimmed. A template that does not use the
  key fields is never altered.
- **Lookup from ralph's own records.** The key comes from the card recorded for the task. Naming a commit never calls the
  task manager and never creates a card.
- **An optional `Card:` trailer.** `loop.commit_card_trailer`, off by default, adds `Card: <key>` after `Ralph-Task`,
  only when the task has a card. It exists because a trailer is machine-readable and survives edits to the subject, which
  the template field does not. It is independent of `loop.commit_trailer` and of the template.
- **Drift matching.** The subject matcher tries a list of candidate subjects: the one rendered with the task's current key,
  then, when the template uses the key and the task has one, the one rendered without it. Commits made before the task had
  a card, or before the template used the key, still match. A commit whose key has since changed cannot be reconstructed
  from ralph's data, so the `Ralph-Task` trailer remains the durable matcher and `loop.commit_trailer` should stay on.

## Alternatives considered

- **Changing the default template to include the key.** Rejected: it would silently change every team's commit messages
  and break subject matching for existing commits.
- **A key in the subject without a trailer option.** Rejected: the subject is the part most often reworded, and a trailer
  keeps the key machine-readable after that.
- **A space or a colon as the prefix.** Rejected: both leave a stray separator or a doubled space when there is no card,
  while a bracketed prefix with its own trailing space disappears cleanly.

## Consequences

- Commit subjects are inconsistent across tasks when only some have cards.
- A task whose card key changes after its commit is matched by its `Ralph-Task` trailer or patch id only; teams that turn
  `loop.commit_trailer` off lose that fallback.
- Every place that names a commit uses the same rendering code.
