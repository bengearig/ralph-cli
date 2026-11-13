# 52. Name a sprint per initiative

- Status: Accepted
- Date: 2026-11-13
- Refines: [49. Attach initiative cards to a chosen parent card](0049-attach-initiative-cards-to-a-chosen-parent-card.md), [51. Add milestone cards to the board's active sprint](0051-add-milestone-cards-to-the-boards-active-sprint.md)

## Context

ADR 51 adds a milestone card to the board's active sprint. Sometimes the work belongs in another sprint, such as the
next one, and which sprint differs from one initiative to the next. As with the parent card in ADR 49, the choice has
to be sayable in any prompt, such as the Receive command. Sprints are renamed and closed over time, and Jira can hold
two open sprints with the same name.

## Decision

- **A per-initiative sprint name, not configuration.** It is stored with the initiative in ralph's store. There is no
  `task_manager.jira` setting for it, and the board stays a team setting.
- **Set through the MCP tools and the CLI.** The `initiative_create` and `initiative_update` tools take an optional
  `sprint`, and `ralph initiative create` and `update` take `--sprint`. An empty value clears it on update. The
  workflows tell the agent to pass it whenever the user names a sprint.
- **Looked up by name among the board's open sprints.** The name is matched exactly, ignoring case and surrounding
  spaces, among the board's active and future sprints. It applies to every milestone card under the initiative and wins
  over the board's active sprint.
- **A fallback with a warning.** A name that matches no open sprint, or more than one, falls back to the board's active
  sprint, and the warning says the named sprint was not usable.
- **Changing it moves no existing card.** The card content hash ignores the sprint, and a card already in an open
  sprint is left alone, as in ADR 51.

## Alternatives considered

- **A sprint ID rather than a name.** Rejected: nobody types or remembers a sprint ID, and a name can be said in a
  prompt.
- **A per-milestone sprint.** Rejected: the initiative is where the user says it, and a milestone's sprint can be
  changed by hand in Jira.
- **Leaving the card in the backlog when the named sprint has closed.** Rejected: a card left unscheduled is worse than
  one in the active sprint, and the warning tells the user why it was not placed as named.
- **A sprint name in config.** Rejected: it is shared by every initiative, and a name changes with every sprint.
- **Reading the sprint from the brief text.** Rejected: the brief is stored verbatim, and guessing a sprint name from
  prose is unreliable.

## Consequences

- A renamed sprint stops matching its old name, so the cards fall back to the active sprint with a warning until the
  initiative's sprint is updated.
- The initiative row has one more column, the sprint name.
- Two open sprints with the same name fall back to the active sprint, with a warning, rather than guess one.
- A future sprint can be named before it starts, and the cards go into it when they are added.
