# Jira

The Jira provider is ralph's first [task manager](glossary.md) provider. It creates and updates Jira cards, one per
initiative, milestone, or task. It targets **Jira Cloud only**: it speaks the REST API v3 and authenticates with Basic
authentication, using an account email and an API token. Jira Server and Data Center are not supported.

Each call to Jira is a single attempt. The provider never retries; a failure is returned to the caller with a hint
about what to check (see [Errors](#errors)).

> **Status.** The provider can create and update cards when it is called, but ralph's commands do not call it yet.
> Creating a resource in ralph does not create a card, and nothing syncs cards or pushes status changes to Jira.
> Wiring the provider into the commands is delivered separately. Until then, the [manual check](#manual-check-against-a-real-project)
> is the way to exercise it against a real project.

See [Configuration](configuration.md#task_manager) for the settings, and
[ADR 21](adr/0021-jira-cloud-as-the-first-task-manager-provider.md) for the decisions behind the provider.

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
  and description are overwritten when ralph pushes changes.
- **Very long bodies.** A body past 30,000 characters is cut, and a note saying it was truncated is added before the
  footer. The full text stays in ralph.

When ralph pushes changes to an existing card it sends only the summary and description, overwriting whatever is there,
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
