# Jira

The Jira provider is ralph's first [task manager](glossary.md) provider. It creates Jira cards for the
kinds of resource you choose (initiatives and milestones by default), keeps them up to date, and moves them through their workflow. It targets **Jira Cloud only**: it speaks the REST API v3 and authenticates with Basic
authentication, using an account email and an API token. Jira Server and Data Center are not supported.

Each call to Jira is a single attempt. The provider never retries; a failure is returned to the caller with a hint
about what to check (see [Errors](#errors)).

See [Configuration](configuration.md#task_manager) for the settings, and
[ADR 21](adr/0021-jira-cloud-as-the-first-task-manager-provider.md) for the decisions behind the provider and
[ADR 24](adr/0024-sync-cards-one-way-with-explicit-backfill.md) for how cards are kept in step.

## Setup

1. Create an API token on the Atlassian account's security page (Account settings, then Security, then API tokens).
   Use an account that can create and edit issues in the target project, and, if `assign_on_start` is on, assign
   them.
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

Creating an initiative, milestone, or task, by any command or MCP tool, creates its card after the resource is saved,
when its kind is listed in `task_manager.card_kinds`. A resource of a kind that is not listed gets no card; see
[Choosing which kinds get cards](#choosing-which-kinds-get-cards).

- **Parents first.** A child card needs its parent's key, so a missing parent card is created before it, when the parent's kind is listed too: a new
  milestone under an initiative that predates Jira creates the initiative's card too, and a new task creates any
  missing milestone and initiative cards. If a parent's card cannot be created, the child's is not attempted, and its
  recorded error names the parent.
- **A failure never blocks ralph.** The resource is saved either way. A Jira failure is printed as a warning (on
  standard error for the CLI, as `card_warning` in the result for `--json` and MCP) and the command still succeeds. The
  one exception is the loop meeting a card held by someone else; see
  [In the loop](#in-the-loop).
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

## Choosing which kinds get cards

`task_manager.card_kinds` lists the kinds of resource that get a card. It is provider-neutral and the default is
`[initiative, milestone]`, so initiatives and milestones get cards and tasks do not.

```yaml
# .ralph/config.yaml
task_manager:
  card_kinds: [initiative, milestone, task]
```

- **Bringing task cards back.** Add `task` to the list, as above. New tasks then get cards, and `ralph cards sync`
  creates the cards for tasks that have none.
- **The parent rule.** A card is a child of its parent's card, so a child kind needs its parent kind: `milestone` needs
  `initiative`, and `task` needs `milestone`. `ralph config validate` refuses a list that breaks the rule, a value that
  is not a kind, and a kind listed twice, whether or not a provider is configured. An empty list is allowed and
  creates no new cards. A list set in a later layer replaces an earlier one.
- **Cards that already exist are kept.** Switching a kind off stops new cards of that kind only. A card that already
  has a key keeps following its resource: edits are pushed, status moves still happen, and deleting the resource still
  moves the card to `canceled_status`. The setting never cancels a card, so a person closes or deletes the cards that
  are no longer wanted in Jira.
- **A failed card of an unlisted kind.** A resource of an unlisted kind whose earlier creation failed, so its card has
  no key, is no longer retried and no longer shown as pending creation. A card with a key is always kept in step.
- **Syncing.** `ralph cards sync` skips resources of unlisted kinds that have no card, reporting them as skipped with
  the reason `kind not in task_manager.card_kinds`. It creates their cards only after the kind is listed. Nothing
  creates them automatically when a kind is added.

[ADR 45](adr/0045-choose-which-kinds-get-jira-cards.md) records the decision.

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
parents are set when the card is created and are not changed afterward. The assignee is left alone too, unless
[`assign_on_start`](#assigning-cards) is on.

Only cards that already exist, or whose creation was attempted and failed, are touched. An edit never creates a card
for work that predates Jira; see [Syncing existing work](#syncing-existing-work).

## Pull request links

When `ralph pr create` ends with a recorded pull request (`opened`, `adopted`, or `exists`), ralph adds a remote link
to the milestone's card, titled `Pull request <repo>#<n>` with the relationship `pull request`. Only the milestone's
card is linked; the initiative and its tasks keep no link, and `ralph pr unlink` does not remove one. A milestone
without a card key, or a configuration without a task manager, is skipped silently.

The link carries a stable `globalId` built from the pull request's provider, namespace, repository, and number, so Jira
updates the existing link instead of adding a second one. That makes the call safe to repeat: rerunning
`ralph pr create` after a failed link (the outcome is then `exists`) tries again. A failure is only a warning, such as
`jira card AIN-3604: pull request link not added: ...`, on standard error; the command's outcome and exit code do not
change. See [`ralph pr create`](cli.md#ralph-pr-create).

Creating a remote link needs the Jira user to be allowed to see and link the issue. Atlassian's documentation for the
remote issue link endpoint names the exact project permissions, but that page could not be read when this was written,
so the permission name is not confirmed here; check the "Add or update remote issue link" entry in the Jira Cloud REST
API reference. A missing permission surfaces as the `pull request link not added` warning.

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

  A value is one status name, or a list of names to pass through in order (see
  [Workflows that gate transitions](#workflows-that-gate-transitions)). Any status that is not listed leaves the card
  where it is. Entries you set merge with the defaults. Add one to move
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

### Workflows that gate transitions

Jira workflows differ from project to project, and the defaults assume the simplest kind: a card can jump straight to
any mapped status. Many workflows do not allow that. A card may start in a holding status such as `To Be Sized`, and
only move on to `To Do`, then `In Progress`, then `Done`, one step at a time. A direct move to `Done` then fails with
`no transition to status "Done"` and the statuses reachable from where the card is.

Nothing here is on by default; a project whose workflow needs it sets it in its own config.

- **A path instead of a name.** A `status_map` value can be a list of Jira statuses to pass through in order:

  ```yaml
  task_manager:
    jira:
      status_map:
        TODO: [To Do]
        IN_PROGRESS: [To Do, In Progress]
        DONE: [To Do, In Progress, Done]
  ```

  Ralph reads the card's current status. If it is in the list, ralph carries on from the step after it, so a card
  that is part way along resumes; otherwise it starts at the first step. Each step must be a transition the workflow
  offers from the previous one. A step that is not stops the move there, leaves the card at the last status it
  reached, and names the statuses reachable from it. The next sync picks up from that point. Ralph cannot read the
  workflow definition, which takes Jira administrator rights, so the path is yours to get right.
- **Required fields.** A transition can require a field, such as an original estimate, that ralph does not have.
  `task_manager.jira.original_estimate` gives ralph a placeholder to use:

  ```yaml
  task_manager:
    jira:
      original_estimate:
        initiative: 1m
        milestone: 1m
        task: 5m
  ```

  Before ralph moves a card it reads the card's estimate, and sets the configured value for the card's kind, found
  from its issue type, only when the card has none. An estimate that is already there is never replaced, and nothing
  is set when the card does not need to move. Leave the settings empty, the default, when the workflow does not
  need an estimate. Any other field a transition requires, which ralph cannot supply, comes back as Jira's error and
  the card stays where it is. Blank the `status_map` entry for that status if ralph should not attempt the move.
- **Seeing the workflow.** `GET /rest/api/3/issue/<KEY>/transitions` lists the moves available to a card from its
  current status, and `GET /rest/api/3/project/<KEY>/statuses` lists a project's statuses.

## Assigning cards

Ralph can assign a card to the person whose API token it uses when work on the card starts. It is opt-in:

```yaml
task_manager:
  jira:
    assign_on_start: true
```

`task_manager.jira.assign_on_start` is `false` by default. With it off, ralph makes no extra Jira calls and leaves the
assignee as people set it.

- **Who.** The user the API token belongs to. Ralph asks Jira who the credential is, through the `myself` endpoint
  (`GET /rest/api/3/myself`), and assigns that account. No account ID is configured. The answer is kept for the life of
  the process.
- **When.** When a task's or a milestone's card is moved to in progress. Initiative cards are never moved by ralph, so
  they are never assigned.
- **Three cases.** A card with no assignee is assigned to the token's user and then moved. A card already assigned to
  the token's user is left as it is and is still moved. A card held by someone else is never overwritten: ralph neither
  assigns nor moves it, and reports an error that names the card and the person who holds it. Interactive commands and
  MCP calls only warn about it, and the loop stops; see [In the loop](#in-the-loop).
- **Blank status mapping.** Assigning runs even when the `IN_PROGRESS` entry of `status_map` is blank, and even when the
  card is already in the in-progress status, so a card that is in progress but unassigned still gets its assignee.
- **Other failures.** If the `myself` call or the assignment call fails, the failure is an ordinary card warning, like
  any other Jira failure (see [When Jira fails](#when-jira-fails)). It is not a conflict.
- **Permission.** Assigning needs the token's user to hold the Assign Issues project permission, in addition to
  permission to create and edit issues. Without it, Jira answers 403 and the card warns.

### In the loop

When the loop starts a task and its card, or its milestone's card, is held by someone else, the loop stops for a
person. This is the one place a Jira condition stops ralph: working a card someone else owns silently is the failure
this prevents.

- **The task is blocked.** The loop moves the task to `BLOCKED` with a reason that names the card and the person who
  holds it, prints the reason in its feed, and stops with the `needs_human` stop reason and exit code `3`. No session is
  started for the task and no failure is counted against the Stall Statute.
- **Everything else only warns.** `ralph task status`, the other interactive commands, and MCP calls such as
  `task_set_status` save the status change and report the conflict as a warning (`card_warning`, with a
  `card_conflict` object naming the card, the resource, and the holder, in the task view a status change returns). They
  never block or fail.
- **What to do.** Reassign the card to the token's user, or unassign it, in Jira. Then move the task back to `TODO`
  (`ralph task status <id> TODO`) and rerun the loop. Until the card is reassigned, the rerun meets the same conflict.

## Deleting

Before an initiative, milestone, or task is deleted, the cards of the resource and of everything the delete removes
with it are moved to `canceled_status`. Nothing is deleted in Jira, so the history stays there. When
`canceled_status` is empty, cards are left alone.

A failed move never stops the delete, because the resource has no record left to retry from. Each card that could not
be moved is named in a warning, for example `warning: card PROJ-12 was not moved: <reason>`, so it can be fixed by
hand. With `--json` and in the MCP tools, the result's `cards` field lists every card with whether it `moved`, and
the error when it did not.

A card whose move failed, or whose resource was deleted before `canceled_status` was set, has no record left in ralph.
[Reconciling orphaned cards](#reconciling-orphaned-cards) finds such cards in Jira and cancels them.

## Reconciling orphaned cards

Deleting a resource removes its card record, so a card whose move failed, or whose resource was deleted before
`canceled_status` was set, has nothing left in ralph to retry from. It stays open in Jira. `ralph cards reconcile`
finds those cards by searching Jira and comparing what it finds with the store.

```sh
ralph cards reconcile                                  # dry run: report only
ralph cards reconcile --created-after 2026-01-01       # dry run, scoped by creation date
ralph cards reconcile --created-after 2026-01-01 --apply
```

- `--created-after YYYY-MM-DD` limits the search to cards created on or after that date. Only the date is sent, and Jira
  reads it in the Jira user's time zone.
- `--apply` cancels the orphans. Without it the command changes nothing.
- `--json` prints the report: counts of each outcome and `items`, each with its `outcome`, `key`, `url`, the parsed `id`
  when there is one, `summary`, `status`, and a `reason` for a skip or a failure.

### The search

The search finds open cards, that is, cards whose status category is not Done, in the configured project that carry
every label in `task_manager.jira.labels`, optionally created on or after `--created-after`. It uses Jira's
`search/jql` endpoint. At least one label must be configured: without labels the search would match every open card in
the project, so the command refuses to run.

### Which cards are orphans

A card's ralph ID is read from the start of its summary, the `ID: title` form ralph writes. The first rule that
matches decides:

| Outcome | When |
|---|---|
| skipped | The card's key is recorded for a resource in ralph. The reason names that resource. |
| skipped | The summary has no ralph ID, so there is nothing to match. |
| skipped | The ID exists in ralph, including archived and cancelled work, under a different key or with no recorded card. |
| orphan | Otherwise: the ID and the key are both unknown to ralph. |

Skipped cards are listed with their reason for a person to review, and are never changed. Cards of archived or
cancelled work are skipped because they are [`ralph cards sync`](#syncing-existing-work)'s business.

### Applying

A dry run is the default and exits `0` whatever it finds. `--apply` is refused unless `--created-after` is given and
`task_manager.jira.canceled_status` is set, and it asks no confirmation: review the dry run first. Each orphan is moved
through the workflow to `canceled_status`, and one line is printed to standard error as each is handled. A card with
no transition to that status is reported as `failed` with the reachable statuses, and is never forced. Nothing is
deleted in Jira.

### The shared-label risk

Labels identify the Jira project, not the ralph store. Another person's cards, or another repository's, that carry the
same labels in the same project have IDs unknown to this store, so they look like orphans, and `--apply` would cancel
them. Review the dry run, and scope the search with `--created-after` to the date this repository started using ralph.

### Permissions and exit codes

The token's user needs the Browse Projects permission for the search and the Transition Issues permission for the move.

Exit code `0` means success, including every dry run. With `--apply`, `3` means at least one card could not be
cancelled, and `1` is any other error, including a missing scope, a missing `canceled_status`, a malformed date, and
a search that failed.

The command is CLI only. There is no MCP tool, so an agent session cannot trigger a bulk cancel.

## Syncing existing work

`ralph cards sync` and the `cards_sync` MCP tool bring cards in line with ralph:

```sh
ralph cards sync [--initiative I-n] [--include-archived] [--verbose]
```

- It first checks the configured issue types against the project: each must exist, and each parent type must sit one
  hierarchy level above its child (see [Hierarchy levels](#hierarchy-levels)). A mismatch stops the sync before
  anything is created or changed.
- It is the only way work that predates Jira gets cards. Ralph never does it automatically, including when a provider
  is first configured or when a kind is added to `card_kinds`, so run it once after setting Jira up.
- It creates missing cards for the kinds in `task_manager.card_kinds`, parents first, retries cards whose creation failed, and brings out-of-date cards up to
  date, status included. Work that is already in sync is not touched, so it is safe to run again.
- Resources of a kind that is not in `card_kinds` and have no card are skipped with the reason
  `kind not in task_manager.card_kinds`; ones that already have a card are brought up to date like any other. See
  [Choosing which kinds get cards](#choosing-which-kinds-get-cards).
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

Jira does not block ralph, with one exception: the loop stops when a card it needs to move is held by someone else (see
[In the loop](#in-the-loop)). Any other failed call is a warning on standard error (or `card_warning` in a result) and the ralph
change still succeeds. The card is recorded as pending, which `show` and `list` display with the last error. Editing
the resource again retries the push, and `ralph cards sync` retries every pending card at once. This holds for the
loop too: a status change it makes never waits on, or fails because of, Jira beyond the time one call can take,
which `task_manager.timeout` bounds.

## After upgrading

Cards are now created only for the kinds in `task_manager.card_kinds`, which defaults to initiatives and milestones.
New tasks stop getting cards, and the cards that tasks already have are untouched: they are still updated and moved.
To keep creating task cards, add `task` to `card_kinds`; see
[Choosing which kinds get cards](#choosing-which-kinds-get-cards).

The card's status is part of its content, so adding it changed the recorded content of every card made before it was
introduced. Those cards show as pending an update until they are next synced, either by an edit to the resource or by
`ralph cards sync`. Nothing is wrong with them.

## Branch names

A branch can start with the milestone's card key, for example `ABC-1234-implement-cool-feature`. This is opt-in: the
default `loop.branch_template` does not use the key, so turning Jira on changes no branch name. Add the key to the
template to use it:

```yaml
# .ralph/config.yaml
task_manager:
  provider: jira
  jira:
    base_url: https://acme.atlassian.net
    project_key: ABC

loop:
  branch_template: "{{.Milestone.CardKeyPrefix}}{{.Milestone.Slug}}"
```

A milestone without a card is named without the key. See [Branch names](loop.md#branch-names) for the fields, the
fallback rules, and why an existing branch is never renamed.

## Commit messages

A task's card key can go in its commit subject and in a `Card:` trailer. Both are off until a team opts in:

```yaml
# .ralph/config.yaml
loop:
  commit_template: "{{.Task.CardKeyPrefix}}{{.Task.ID}}: {{.Task.Title}}"
  commit_card_trailer: true
```

A commit for a task with card `ABC-1235` then reads `[ABC-1235] T-12: Add the Jira client`, with `Card: ABC-1235` in its
trailers; a task without a card gets `T-12: Add the Jira client` and no `Card:` trailer. Jira links a commit to a card
when its message mentions the card key and the repository is connected to Jira through a development-tool
integration. See [Card keys in commits](loop.md#card-keys-in-commits) for the fields, the cleaning rule, and how the
drift check copes with keys that appear later.

## Card types and why

Ralph fixes the hierarchy: an initiative's card is the top level, a milestone's card is its child, and a task's card
is a child of its milestone's card. Which kinds get a card is set by
[`task_manager.card_kinds`](#choosing-which-kinds-get-cards); the Jira type names are configurable too:

| Setting | Default | Parent |
|---|---|---|
| `task_manager.jira.issue_types.initiative` | `Epic` | none |
| `task_manager.jira.issue_types.milestone` | `Task` | the initiative's card |
| `task_manager.jira.issue_types.task` | `Sub-task` | the milestone's card |

The defaults are Jira's built-in hierarchy, which exists in every standard project, so the provider works without any
project setup. Jira has no built-in Feature type. A team that has added one sets it by name, for example
`issue_types.initiative: Feature`. The configured type must exist in the project, and Jira must allow it at that
level of the hierarchy under the parent's type.

### Hierarchy levels

Every Jira issue type sits on a hierarchy level, and a parent must be **exactly one level above** its child. The
defaults assume Epic is directly above Task, as in a standard project. A project with extra levels breaks that. For
example, a project whose hierarchy is Initiative (3), Epic (2), Feature (1), Task (0), and Sub-task (-1) cannot put a
Task under an Epic, and Jira answers `parent: Please select valid parent issue`. There the types are set to match:

```yaml
task_manager:
  jira:
    issue_types:
      initiative: Feature
      milestone: Task
      task: Sub-task
```

To see a project's types and levels, read `issueTypes[].hierarchyLevel` from
`GET /rest/api/3/project/<KEY>`. `ralph cards sync` does this itself before it creates anything and stops with a
message naming the mismatch; see [Syncing existing work](#syncing-existing-work).

Changing `issue_types` does not retype cards that already exist. Change those in Jira, for example with a bulk move,
and ralph keeps their keys.

A milestone's card cannot be created before its initiative's card, nor a task's before its milestone's: the provider
refuses a request that has no parent card key.

## What a card looks like

- **Summary.** `<ID>: <title>`, for example `T-12: Add the login form`. It is cut to 255 characters, ending with an
  ellipsis when it is cut, which is the longest summary Jira accepts.
- **Labels.** Only those in `task_manager.jira.labels`; ralph adds none of its own, and none is added with no
  setting. The resource's ID is in the summary, not in a label. Configured labels are added when a card is created, so
  cards made before a label was configured do not get it.
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
to a card are kept, as are its status and every other field. The assignee is kept as well, except that ralph assigns
an unassigned card to the token's user when it moves the card to in progress and
[`assign_on_start`](#assigning-cards) is on.

## Errors

A failed call returns an error naming the method, the path, the status, and the messages Jira gave, followed by a hint
where one applies. Ralph does not retry; fix the cause and run the operation again.

| Status | Likely cause | What to check |
|---|---|---|
| 401, 403 | The email or token is wrong, or the user may not create issues in the project, or, with `assign_on_start` on, may not assign them. | The email and API token, and that the token's user can create issues in the project and, when assigning, holds the Assign Issues permission. |
| Assignment error naming the holder | With `assign_on_start` on, the card is already assigned to someone else, so ralph neither assigned nor moved it. A warning in interactive commands and MCP calls; in the loop it blocks the task and stops the run. | Who holds the card in Jira. Reassign it there, or unassign it, then move a blocked task back to `TODO`; see [In the loop](#in-the-loop). |
| 404 | The site or project does not exist, or the account cannot see it. | `task_manager.jira.base_url` and `task_manager.jira.project_key`. |
| 400 mentioning an issue type or a field | The project lacks an issue type, the type is not allowed under the parent, or a required field is missing. | `task_manager.jira.issue_types` against the project's issue types, and the project's required fields. |
| 400 `Please select valid parent issue` | The child's issue type is not exactly one hierarchy level below its parent's type. | The levels of the types in `task_manager.jira.issue_types`; see [Hierarchy levels](#hierarchy-levels). |
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
         "labels": ["workflow-2.0"]
       }
     }'
   ```

   A `201` response with a `key` means the credentials, project, and Epic type work. A child card adds
   `"parent": {"key": "<parent key>"}` to `fields`.
3. Create an Epic, a Task under it, and a Sub-task under that, through the provider (a short Go test or program that
   builds a provider with `jira.NewProvider` and calls `Create` for each, passing the parent's key from the previous
   call). Confirm in Jira:
   - the returned keys exist, and each returned URL opens its card;
   - each card has exactly the configured labels, and no label with its ID;
   - the Task's parent is the Epic and the Sub-task's parent is the Task;
   - the description renders: headings, lists, a code block, bold and italic text, and a link appear as such, a table
     appears as plain text, and the footer is last. Include each in the body you send.
4. Call `Update` on one card with a new title and body. Confirm that only the summary and description changed, and that
   a label you added by hand in Jira, the status, and the parent are unchanged.
5. Delete the scratch cards, or the scratch project.
