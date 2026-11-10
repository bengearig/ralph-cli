# Jira

The Jira provider is ralph's first [task manager](glossary.md) provider. It creates Jira cards, one per
initiative, milestone, or task, keeps them up to date, and moves them through their workflow. It targets **Jira Cloud only**: it speaks the REST API v3 and authenticates with Basic
authentication, using an account email and an API token. Jira Server and Data Center are not supported.

Each call to Jira is a single attempt. The provider never retries; a failure is returned to the caller with a hint
about what to check (see [Errors](#errors)).

See [Configuration](configuration.md#task_manager) for the settings, and
[ADR 21](adr/0021-jira-cloud-as-the-first-task-manager-provider.md) for the decisions behind the provider and
[ADR 23](adr/0023-sync-cards-one-way-with-explicit-backfill.md) for how cards are kept in step.

## Setup

1. Create an API token on the Atlassian account's security page (Account settings, then Security, then API tokens).
   Use an account that can create and edit issues in the target project.
2. Export the account email and the token. The variable names are the ones configured in
   `task_manager.jira.email_env` and `task_manager.jira.token_env`; the defaults are `RALPH_JIRA_EMAIL` and
   `RALPH_JIRA_TOKEN`:

   ```sh
   export RALPH_JIRA_EMAIL="you@example.com"
   export RALPH_JIRA_TOKEN="<api token>"
   ```

3. Set `task_manager.jira.base_url` (the site, such as `https://acme.atlassian.net`) and
   `task_manager.jira.project_key` (the project that holds the cards).
4. Check that the project has issue types matching `task_manager.jira.issue_types`. The defaults are `Epic`, `Task`,
   and `Sub-task`, in the hierarchy Epic, then Task, then Sub-task, which every standard project has.
5. Select the provider and validate the configuration:

   ```yaml
   # .ralph/config.yaml
   task_manager:
     provider: jira
     jira:
       base_url: https://acme.atlassian.net
       project_key: STORE
   ```

   ```sh
   ralph config validate
   ```

   Validation checks the settings only. It makes no network call, so it cannot tell whether the token works or the
   project has the issue types; the first call to Jira reports those.

The settings and their defaults are listed under [`task_manager`](configuration.md#task_manager) in the configuration
reference.

## When cards are created

Creating an initiative, milestone, or task, by any command or MCP tool, creates its card after the resource is saved.

- **Parents first.** A child card needs its parent's key, so a missing parent card is created before it: a new
  milestone under an initiative that predates Jira creates the initiative's card too, and a new task creates any
  missing milestone and initiative cards. If a parent's card cannot be created, the child's is not attempted, and its
  recorded error names the parent.
- **A failure never blocks ralph.** The resource is saved either way. A Jira failure is printed as a warning (on
  standard error for the CLI, as `card_warning` in the result for `--json` and MCP) and the command still succeeds.
  The card then shows as pending creation, with the last error, until a later attempt succeeds.
- **No duplicates across processes.** Before calling Jira, a process claims the card in ralph's store. If another
  process, such as the loop's MCP server beside a person's CLI, holds a recent claim, this one skips creation instead
  of creating a second card. A claim that is never completed, for example because its process was killed, is taken over
  once it is stale.
- **A misconfigured provider disables cards, not ralph.** If the provider cannot be built or its configuration is
  invalid, such as a missing environment variable, ralph prints `task manager disabled: <reason>` once on standard
  error and carries on as if no provider were configured. `ralph config validate` prints the same line. The MCP server
  reports it once at start.
- **Inside a transaction.** Work done in a single transaction, such as a bulk import, does not create cards.

## Seeing cards

`ralph initiative show`, `milestone show`, and `task show` print a `card:` line with the key and URL, followed by
`(pending create)` or `(pending update)` when the card is behind, and the last error on the next line when there is
one. List commands gain a `CARD` column when a provider is configured, and `--json` and the MCP tools carry a `card`
object. With no provider configured, nothing about cards is shown. A resource that has never had a card attempt has no
card line.

Whether a card is pending is worked out when it is shown, not stored: a card with no key needs creating, and a card
whose stored content hash differs from what ralph would send now needs updating. See
[Card records](data-model.md#card-records).

## Pushing changes

Editing an initiative's title or brief, a milestone's title or description, or a task's title, description, or
acceptance criteria updates
the card's summary and description. Ralph sends only those two fields, and the sync is one way: Jira is never read
back, so an edit made in Jira to the summary or description is overwritten the next time ralph pushes. Labels and
parents are set when the card is created and are not changed afterward.

Only cards that already exist, or whose creation was attempted and failed, are touched. An edit never creates a card
for work that predates Jira; see [Syncing existing work](#syncing-existing-work).

## Status

A card follows the status of its resource, so a board in Jira shows where the work stands.

- **Which cards move.** Task cards follow the task's status. Milestone cards follow the milestone's computed status,
  the one `milestone show` and `status` print, which is recomputed whenever one of its tasks changes. Initiative cards
  never move.
- **`task_manager.jira.status_map`.** Maps a ralph status to the name of a Jira status. The defaults are Jira's
  standard names:

  ```yaml
  task_manager:
    jira:
      status_map:
        TODO: To Do
        IN_PROGRESS: In Progress
        DONE: Done
  ```

  Any status that is not listed leaves the card where it is. Entries you set merge with the defaults. Add one to move
  more statuses, for example `BLOCKED: Blocked`, and blank one to stop a default from moving cards, for example
  `DONE: ""`. A project whose workflow uses other names sets them here. `CANCELLED` is not allowed in the map; see
  below.
- **How a move works.** Ralph reads the issue's current status. If it already has the target name, nothing is done.
  Otherwise ralph reads the issue's workflow transitions and performs the one whose target status has that name,
  ignoring case. If no transition leads there, for example because the workflow does not allow the jump from the
  current status, the move fails with an error that lists the statuses reachable from where the issue is, so the map
  or the workflow can be fixed. The match is by the status the transition ends in, not by the transition's own name.
- **Cancelled work.** A cancelled task's card moves to `task_manager.jira.canceled_status`. It is empty by default,
  which leaves the card alone, because Jira's standard workflows have no cancelled status and naming one that does not
  exist would only produce failures. Set it to a status your workflow has, such as `Cancelled`.
- **A new card.** A card is created, and then moved to its resource's status. If the move fails, the card still
  exists, shows as pending an update, and the move is retried by the next edit or sync.

## Deleting

Before an initiative, milestone, or task is deleted, the cards of the resource and of everything the delete removes
with it are moved to `canceled_status`. Nothing is deleted in Jira, so the history stays there. When
`canceled_status` is empty, cards are left alone.

A failed move never stops the delete, because the resource has no record left to retry from. Each card that could not
be moved is named in a warning, for example `warning: card PROJ-12 was not moved: <reason>`, so it can be fixed by
hand. With `--json` and in the MCP tools, the result's `cards` field lists every card with whether it `moved`, and
the error when it did not.

## Syncing existing work

`ralph cards sync` and the `cards_sync` MCP tool bring cards in line with ralph:

```sh
ralph cards sync [--initiative I-n] [--include-archived] [--verbose]
```

- It is the only way work that predates Jira gets cards. Ralph never does it automatically, including when a provider
  is first configured, so run it once after setting Jira up.
- It creates missing cards, parents first, retries cards whose creation failed, and brings out-of-date cards up to
  date, status included. Work that is already in sync is not touched, so it is safe to run again.
- Cancelled work that has no card is skipped, so abandoned work does not fill the project. A cancelled task that
  already has a card is brought up to date like any other.
- `--initiative` limits the run to one initiative. Archived initiatives are skipped unless `--include-archived` is
  given. `--verbose` also lists the items that were already in sync. The tool takes `initiative_id` and
  `include_archived`.
- The report counts what was created, updated, left in sync, skipped, and failed, and lists each created, updated,
  failed, and skipped item with its card key and reason. `--json` and the tool return the same report.
- Exit code `0` means nothing failed, `3` means at least one item failed and a person needs to look at the errors in
  the report, and `1` is any other error, such as no provider being configured.

A sync is deliberate, so it ignores the recent claim that holds off a second creation attempt (see
[When cards are created](#when-cards-are-created)), but it still takes the claim, so two syncs cannot create the same
card.

## When Jira fails

Jira never blocks ralph. A failed call is a warning on standard error (or `card_warning` in a result) and the ralph
change still succeeds. The card is recorded as pending, which `show` and `list` display with the last error. Editing
the resource again retries the push, and `ralph cards sync` retries every pending card at once. This holds for the
loop too: a status change it makes never waits on, or fails because of, Jira beyond the time one call can take,
which `task_manager.timeout` bounds.

## After upgrading

The card's status is part of its content, so adding it changed the recorded content of every card made before it was
introduced. Those cards show as pending an update until they are next synced, either by an edit to the resource or by
`ralph cards sync`. Nothing is wrong with them.

## Card types and why

Ralph fixes the hierarchy: an initiative's card is the top level, a milestone's card is its child, and a task's card
is a child of its milestone's card. Only the Jira type names are configurable:

| Setting | Default | Parent |
|---|---|---|
| `task_manager.jira.issue_types.initiative` | `Epic` | none |
| `task_manager.jira.issue_types.milestone` | `Task` | the initiative's card |
| `task_manager.jira.issue_types.task` | `Sub-task` | the milestone's card |

The defaults are Jira's built-in hierarchy, which exists in every standard project, so the provider works without any
project setup. Jira has no built-in Feature type. A team that has added one sets it by name, for example
`issue_types.initiative: Feature`. The configured type must exist in the project, and Jira must allow it at that
level of the hierarchy under the parent's type.

A milestone's card cannot be created before its initiative's card, nor a task's before its milestone's: the provider
refuses a request that has no parent card key.

## What a card looks like

- **Summary.** `<ID>: <title>`, for example `T-12: Add the login form`. It is cut to 255 characters, ending with an
  ellipsis when it is cut, which is the longest summary Jira accepts.
- **Labels.** `ralph` and the resource's ID. The `ralph` label finds every card ralph made; the ID label finds one.
- **Description.** The resource's Markdown body, converted to Jira's Atlassian Document Format (ADF), the structured
  rich text that Jira Cloud's API requires. The converter is small and lives in the repository; it handles common
  Markdown:
  - headings, paragraphs, and line breaks within a paragraph;
  - bulleted and numbered lists, including nested lists;
  - fenced code blocks, with their language;
  - block quotes and horizontal rules;
  - bold, italic, inline code, and links.

  Anything the converter does not understand is kept as plain text, and the converter never drops text. Tables, HTML,
  and images therefore appear as their source text rather than as Jira tables or markup. Links with a scheme other
  than `http`, `https`, or `mailto` appear as their label without the link.
- **Footer.** Every description ends with a rule and a line saying that ralph manages the card and that the summary
  and description are overwritten when ralph updates the card.
- **Very long bodies.** A body past 30,000 characters is cut, and a note saying it was truncated is added before the
  footer. The full text stays in ralph.

When ralph updates an existing card it sends only the summary and description, overwriting whatever is there,
so edit those in ralph rather than in Jira. It does not touch anything else, including labels: labels that people add
to a card are kept, as are its status, assignee, and every other field.

## Errors

A failed call returns an error naming the method, the path, the status, and the messages Jira gave, followed by a hint
where one applies. Ralph does not retry; fix the cause and run the operation again.

| Status | Likely cause | What to check |
|---|---|---|
| 401, 403 | The email or token is wrong, or the user may not create issues in the project. | The email and API token, and that the token's user can create issues in the project. |
| 404 | The site or project does not exist, or the account cannot see it. | `task_manager.jira.base_url` and `task_manager.jira.project_key`. |
| 400 mentioning an issue type or a field | The project lacks an issue type, the type is not allowed under the parent, or a required field is missing. | `task_manager.jira.issue_types` against the project's issue types, and the project's required fields. |
| 429 | Jira is rate limiting the account. | Wait and run again. The hint includes Jira's `Retry-After` value when Jira sends one. |
| Timeout or network failure | Jira did not answer within `task_manager.timeout`, or the site could not be reached. | The network, the `base_url`, and `task_manager.timeout`. There is no status because no response arrived. |

A missing credential is reported before any request, naming the environment variable and the setting that names it.

## Security

- Config files hold only the **names** of the environment variables, never the email or the token.
- The token is held in memory for the Authorization header. It does not appear in errors or logs; error messages
  carry the method, path, status, and Jira's own messages.

## Manual check against a real project

Automated tests use a fake server, so run this once against a real Jira Cloud site before relying on the provider.
Use a scratch project, because the check creates cards.

1. Export `RALPH_JIRA_EMAIL` and `RALPH_JIRA_TOKEN` (or the names you configured), and point `base_url` and
   `project_key` at the scratch project. Run `ralph config validate`.
2. Confirm the credentials and the create request with `curl`, which is what the provider sends for an Epic:

   ```sh
   curl --user "$RALPH_JIRA_EMAIL:$RALPH_JIRA_TOKEN" \
     --request POST "https://acme.atlassian.net/rest/api/3/issue" \
     --header "Content-Type: application/json" \
     --header "Accept: application/json" \
     --data '{
       "fields": {
         "project": {"key": "SCRATCH"},
         "issuetype": {"name": "Epic"},
         "summary": "I-1: Manual check",
         "description": {"version": 1, "type": "doc", "content": [
           {"type": "paragraph", "content": [{"type": "text", "text": "Hello from ralph."}]}
         ]},
         "labels": ["ralph", "I-1"]
       }
     }'
   ```

   A `201` response with a `key` means the credentials, project, and Epic type work. A child card adds
   `"parent": {"key": "<parent key>"}` to `fields`.
3. Create an Epic, a Task under it, and a Sub-task under that, through the provider (a short Go test or program that
   builds a provider with `jira.NewProvider` and calls `Create` for each, passing the parent's key from the previous
   call). Confirm in Jira:
   - the returned keys exist, and each returned URL opens its card;
   - each card has the labels `ralph` and its ID;
   - the Task's parent is the Epic and the Sub-task's parent is the Task;
   - the description renders: headings, lists, a code block, bold and italic text, and a link appear as such, a table
     appears as plain text, and the footer is last. Include each in the body you send.
4. Call `Update` on one card with a new title and body. Confirm that only the summary and description changed, and that
   a label you added by hand in Jira, the status, and the parent are unchanged.
5. Delete the scratch cards, or the scratch project.
