# 23. Card keys in branch names

- Status: Accepted
- Date: 2026-11-10
- Refines: [9. Branch and pull request per milestone](0009-branch-and-pull-request-per-milestone.md)

## Context

Teams that track work in Jira often want the card key in the branch name, such as `ABC-1234-implement-cool-feature`,
so branches link to cards. Ralph already records a card for each milestone ([ADR 22](0022-create-cards-when-resources-are-created.md)).
A milestone's branch name is computed by the loop, `ralph pr create`, and the drift check. Some milestones
have no card: the provider is off, card creation failed, or the milestone predates Jira.

## Decision

- **Opt-in through the template.** `loop.branch_template` gains key fields. The default template does not use them, so
  turning on a task manager changes no branch name.
- **Key fields on both entities.** `.Initiative` and `.Milestone` each get `.CardKey`, the recorded card key or empty,
  and `.CardKeyPrefix`, the key followed by `-` or empty.
- **Lookup from ralph's own records.** One function reads the keys recorded for the configured provider, and all three
  callers use it. Naming a branch never calls the provider and never creates a card. A lookup error leaves the keys
  empty rather than failing.
- **Clean fallback.** When a key is empty and the template uses a key field, leading separators are removed from each
  path component and repeated dashes collapse, so no stray dash is left. A template that does not use the key fields is
  never altered.
- **Invalid-key fallback.** If the name rendered with the keys is not a valid git branch name, it is rendered again
  with empty keys instead of failing.
- **The recorded branch still wins.** A milestone that already has a branch is never renamed when its card appears
  later.
- **No key fields in commit templates.** The drift check matches commits by their rendered subject. A key that
  appeared or changed after a commit was made would change the subject and break the match, so `loop.commit_template`
  and the prompt template keep their existing fields.

## Alternatives considered

- **A new default template when a task manager is on.** Rejected: enabling Jira would silently change branch names,
  and existing teams did not ask for that.
- **Refusing to name a branch without a card.** Rejected: ralph must keep working when the tracker is unreachable or
  the milestone predates it.
- **Trying to create the card before branching.** Rejected: it puts a network call and its failures into branch
  naming, which would then differ between the loop, `ralph pr create`, and the drift check.
- **Exposing only the raw key.** Rejected: a template with a separator after the key would leave a stray separator
  when the key is empty, and the prefix field avoids that.

## Consequences

- Branch names are inconsistent across milestones when only some have cards.
- A milestone gets a key in its branch only if its card exists in ralph when the branch is first named.
- Every place that shows a branch uses the same computed name.
