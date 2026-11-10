# 21. Jira Cloud as the first task manager provider

- Status: Accepted
- Date: 2026-11-10
- Refines: [20. Task manager abstraction and card records](0020-task-manager-abstraction-and-card-records.md)

## Context

The task manager abstraction needs a first provider, and Jira is the tracker the team uses. The provider has to decide
how a card identifies the ralph resource it belongs to, how Markdown bodies reach Jira, how failures are handled, and
what an update may change.

## Decision

- **Jira Cloud only.** The provider uses the REST API v3 with Basic authentication: an account email and an API token,
  each read from an environment variable named in config. Jira Server and Data Center are out of scope.
- **Summary prefix plus a label.** A card's summary is `<ID>: <title>`, and it carries the label `ralph`. The prefix
  makes a card recognizable in any list and the label makes cards searchable, without needing anything configured in
  the Jira project. The ID is deliberately not a label, so labels stay a small, meaningful set; extra labels come from
  `task_manager.jira.labels`.
- **A small in-repo Markdown to ADF converter.** Jira Cloud's v3 API takes rich text as Atlassian Document Format. A
  converter in `internal/taskmanager/jira/adf` handles common Markdown (headings, lists, code blocks, quotes, rules,
  and inline emphasis, code, and links) and keeps anything else as plain text, never dropping text. It adds no
  dependency.
- **One attempt, no retries, in the client.** A call to Jira is made once and a failure is returned with a hint about
  what to check. Whether and when to try again is the caller's decision, because the sync knows what is pending and
  can retry later without holding anything open.
- **Update sends only the summary and description.** Those are the fields ralph owns. Labels, status, assignee, and
  everything else are left as people set them. The footer on each description says ralph manages the card and
  overwrites those two fields.

## Alternatives considered

- **Labels only, with no summary prefix.** Rejected: a card would be anonymous in lists and boards, and a person
  could not tell which ralph resource it was without opening it.
- **A custom field for the ID.** Rejected: it needs administrator setup in every project, which defeats a provider that
  should work against a standard project, and custom fields differ between projects.
- **Plain paragraphs for the description.** Rejected: headings, lists, and code in ralph's Markdown bodies would
  arrive as run-together text.
- **The whole body in a single code block.** Rejected: it preserves the text but shows the source, not the formatting,
  and reads badly in Jira.
- **A third-party Markdown converter.** Rejected: ADF output needs mapping code either way, and a dependency adds
  weight and a maintenance surface for a bounded set of constructs.
- **Retrying inside the client.** Rejected: hidden retries make a command slow and unpredictable, and they duplicate
  what the sync does when it finds a card still pending.
- **Updating every field, or only the fields that changed.** Rejected: sending more than the summary and description
  would overwrite what people edit in Jira, and diffing against Jira would need reads that ralph does not otherwise
  make.

## Consequences

- Constructs the converter does not handle, such as tables and HTML, show in Jira as plain source text until the
  converter grows to cover them.
- Rate limits and timeouts surface as errors to the caller. The caller must decide when to run the operation again.
- Edits made to a card's summary or description in Jira are lost the next time ralph pushes that card, and the footer
  says so. Labels added by people survive.
- A project must have issue types matching the configured names, or Jira rejects the request and the error says so.
- The provider can create and update cards. Ralph's commands call it to create cards ([ADR 22](0022-create-cards-when-resources-are-created.md));
  pushing later edits and status changes is covered by [ADR 24](0024-sync-cards-one-way-with-explicit-backfill.md).
