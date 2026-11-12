# 40. Assign Jira cards to the token's user

- Status: Accepted
- Date: 2026-11-12
- Refines: [21. Jira Cloud as the first task manager provider](0021-jira-cloud-as-the-first-task-manager-provider.md)

## Context

[ADR 21](0021-jira-cloud-as-the-first-task-manager-provider.md) has the provider send only a card's summary and
description, and leave the assignee as people set it. That keeps ralph from overwriting what people do in Jira, but a
card moved to in progress by ralph's work stays unassigned, so a board does not show who is working on it. A team that
wants the assignee filled in needs it done when the card moves, without ralph taking cards from people who hold them.

## Decision

- **Opt-in.** `task_manager.jira.assign_on_start` is a boolean, off by default. With it off, no extra Jira call is made
  and the assignee stays as people set it.
- **Which cards.** Task and milestone cards, when the provider moves the card to in progress. Initiative cards are not
  included: ralph never moves them ([ADR 24](0024-sync-cards-one-way-with-explicit-backfill.md)), so there is no event
  to hook, and they stay as people set them.
- **Who.** The user the API token belongs to. The provider asks Jira who the credential is (`GET /rest/api/3/myself`)
  and assigns that account. No account ID is configured, and the answer is kept for the life of the provider.
- **When it runs.** For the in-progress status only, before the card is moved, and whether or not the card still needs
  moving, so a card already in the in-progress status can still be assigned. It runs even when the in-progress entry of
  `status_map` is blank.
- **Existing assignee.** A card already assigned to the token's user is left alone and is still moved. A card assigned to
  someone else is never overwritten: the provider returns a typed conflict carrying the card key and the holder's
  display name, and neither assigns nor moves the card. The conflict is reported as an error.
- **Other failures stay warnings.** A failure of the `myself` call or the assignment call, including a 403 for a missing
  Assign Issues permission, is an ordinary card warning and not a conflict.

## Alternatives considered

- **A configured account ID.** Rejected: it is one more setting to find and keep right, it can disagree with the
  credential in use, and the token already says whose work this is.
- **Overwriting an existing assignee.** Rejected: silently taking a card from the person who holds it, or working a
  card someone else owns, is the failure this decision exists to prevent.
- **Assigning always, with no setting.** Rejected: it changes what ralph does to every existing Jira setup and needs a
  permission those users may not have granted.
- **Assigning initiative cards.** Rejected: ralph never moves them, so there is no moment that means work has started.

## Consequences

- The statement in ADR 21 that the assignee is left as people set it holds only while `assign_on_start` is off.
- Assigning needs the token's user to hold the Assign Issues project permission, in addition to the permissions
  already needed to create and edit cards.
- A card held by someone else stays where it is and is reported as an error until it is reassigned in Jira.
- The loop is the one place a Jira condition stops ralph. When it starts a task whose card, or whose milestone's card, is
  held by someone else, it moves the task to `BLOCKED` with a reason naming the card and the holder, starts no session,
  counts no failure, and stops needing a human. Interactive commands and MCP calls still only warn. The loop is
  singled out because it works unattended: a warning there would go unseen while an agent did the work of a card another
  person owns, whereas a person at a command line sees the warning and decides. This is a narrow exception to the rule
  that a Jira failure never blocks ralph ([ADR 22](0022-create-cards-when-resources-are-created.md)).
- A failed move leaves the card pending, so a later push or `ralph cards sync` records the new state.
