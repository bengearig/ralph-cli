# Bitbucket Cloud

The Bitbucket Cloud provider is ralph's first pull request provider. `ralph pr create` uses it to open the pull request
for a milestone's branch. It targets **Bitbucket Cloud only**, through the REST API 2.0; Bitbucket Data Center is not
supported.

Each call to Bitbucket is a single attempt. The provider never retries; a failure is returned with a hint about what to
check (see [Errors](#errors)).

See [Configuration](configuration.md#pr) for the settings, [`ralph pr create`](cli.md#loop-and-pull-requests) for the
command, and [ADR 34](adr/0034-record-pull-requests-without-urls.md) for the decisions behind the provider.

## Setup

1. Choose how ralph authenticates (see [Authentication](#authentication)) and create the credential.
2. Export the credential. The variable names are the ones configured in `pr.bitbucket_cloud.email_env` and
   `pr.bitbucket_cloud.token_env`; the defaults are `RALPH_BITBUCKET_EMAIL` and `RALPH_BITBUCKET_TOKEN`:

   ```sh
   export RALPH_BITBUCKET_EMAIL="you@example.com"
   export RALPH_BITBUCKET_TOKEN="<api token>"
   ```

   With `bearer`, only the token variable is needed.
3. Set `pr.bitbucket_cloud.workspace` and `pr.bitbucket_cloud.repo_slug`, and `pr.base_branch` if the pull request
   should not target `main`:

   ```yaml
   # .ralph/config.yaml
   pr:
     base_branch: develop
     bitbucket_cloud:
       workspace: acme
       repo_slug: storefront
   ```

4. Validate the configuration:

   ```sh
   ralph config validate
   ```

   Validation makes no network call, so it cannot tell whether the credential works. It reports every missing setting
   at once: the workspace, the repository slug, the token variable, and, for `basic`, the email variable. Each message
   names the setting.

The settings and their defaults are listed under [`pr`](configuration.md#pr) in the configuration reference.

## Authentication

`pr.bitbucket_cloud.auth` selects one of two modes.

- **`basic`, the default.** HTTP Basic authentication with the Atlassian account email, read from the variable named by
  `email_env`, and an Atlassian API token, read from the variable named by `token_env`. This is the same shape as the
  [Jira](jira.md) provider, so one account email and token pair can serve both. It is the default because Bitbucket
  app passwords no longer work, which leaves the API token as the credential a person is most likely to have.
- **`bearer`.** An `Authorization: Bearer <token>` header with the token alone, read from the variable named by
  `token_env`. Use it for a repository access token or a workspace access token, for example in automation that should
  not act as a person. `email_env` is not used.

The configuration holds only the **names** of the environment variables. The email and the token are read when a
request is made and are never written to a config file, the store, or an error message.

### Credential scopes

The credential has to read and create pull requests. Atlassian's documentation of
[API token permissions](https://support.atlassian.com/bitbucket-cloud/docs/api-token-permissions/) names these scopes:

| Scope | What Atlassian says it allows |
|---|---|
| `read:pullrequest:bitbucket` | Viewing pull requests. |
| `write:pullrequest:bitbucket` | Creating, updating, approving, declining, and merging pull requests. |
| `read:repository:bitbucket` | Viewing repositories. Pull request scopes do not include it. |

The scope names above were checked against that page. **What was not confirmed** is the exact mapping from the two
endpoints ralph calls, creating a pull request and listing open ones, to these scopes: the page describes each scope
but does not map endpoints to them. Creating is expected to need the pull request write scope, and finding an existing
pull request the pull request read scope, with the repository read scope as the likely companion. If a call is
refused with `403`, add the missing scope and try again.

A repository access token chooses its permissions from a different list, described in
[repository access token permissions](https://support.atlassian.com/bitbucket-cloud/docs/repository-access-token-permissions/):
a `pullrequest` read permission, a `pullrequest:write` permission, and `repository` read. Give such a token the pull
request write permission and the repository read permission.

## What ralph sends

`ralph pr create` first checks that the milestone's branch matches its recorded state and that the milestones it depends
on are merged; see [`ralph pr create`](cli.md#loop-and-pull-requests). It then creates the pull request.

- **Finding.** The provider can look for an existing pull request: a `GET` of the repository's pull requests, limited to
  open ones whose source branch is the milestone branch **and whose destination branch is `pr.base_branch`**, most
  recently updated first. Only the first page is read. Adopting an existing pull request therefore requires the
  destination to equal `pr.base_branch`: an open pull request from the same branch into a different branch is not
  adopted, so the pull request the milestone needs can still be opened. `ralph pr create` does not call this yet; it
  comes with recording the pull request, described below.
- **Creating.** A `POST` to the repository's pull requests with the title, the description (the request body), the
  source and destination branch names, and whether it is a draft. The response gives the pull request's number and web
  link.
- **Headers.** `Authorization`, `Accept` and `Content-Type` as JSON, and a `User-Agent` naming ralph and its version.

Each request is bounded by `pr.timeout`. Responses are capped in size, and a body quoted in an error is cut short.
Reviewers, labels, closing the source branch, updating or merging a pull request, further result pages, and Bitbucket
Data Center are not handled.

`ralph pr create` pushes the milestone's branch to `pr.remote` itself, so the branch does not have to be pushed by
hand. The push is plain and never forced. Before creating anything it asks Bitbucket for an open pull request from the
branch into `pr.base_branch`: if one exists, it is adopted and recorded instead of creating a duplicate, and a pull
request from the same branch into a different branch is not adopted. See [`ralph pr create`](cli.md#ralph-pr-create).

## Errors

A failed call returns an error that begins `bitbucket_cloud: <method> <path>:` and names the cause.

| Status | Message | What to check |
|---|---|---|
| 401 | `authentication failed (401)`; names `pr.bitbucket_cloud.auth` and the credential settings. | The auth mode, the email and token variables, and that the token has not expired. |
| 403 | `permission denied (403)`; the credential lacks pull request write access. | The credential's [scopes](#credential-scopes). |
| 404 | `repository <workspace>/<repo_slug> not found or not accessible (404)`. | `pr.bitbucket_cloud.workspace` and `repo_slug`, and that the credential can see the repository. |
| 400 | `bad request (400)` with the API's message and field errors, then a hint to check that the source branch has been pushed. | That the destination branch exists, and that the push to `pr.remote` reached the repository configured here. |
| 429 | `rate limited (429)`, with `Retry-After` when Bitbucket sends it. | Wait and run again. |
| other | The status code and text, with the API's message when there is one. | The message. |
| none | A timeout or network failure, wrapped with the method and path. | The network and `pr.timeout`. |

A missing credential is reported before any request: `environment variable <NAME> (<setting>) is not set`, naming the
variable and the setting that names it. Errors never contain the token, the email, or the `Authorization` header.

## Manual check against a real repository

Automated tests use a fake server, so check once against a real repository before relying on the provider. Use a
scratch repository, because the last step opens a pull request.

1. Export the credential and set `workspace`, `repo_slug`, and `pr.base_branch`. Run `ralph config validate`.
2. Check that `pr.remote` names the remote of the repository you configured. `ralph pr create` pushes the branch there.
3. Print what would be sent:

   ```sh
   ralph pr create --milestone M-1 --dry-run
   ```

   It prints the provider, the title, the branches, the drift check's result, the push that would happen, and the body. A configuration problem,
   such as a missing environment variable, is printed as a warning. The dry run pushes nothing, sends nothing, and needs no network.
4. Open the pull request for real:

   ```sh
   ralph pr create --milestone M-1
   ```

   It pushes the branch and prints `opened <url>` on success. Open the link and confirm the title, the description, the source and
   destination branches, and that the pull request is a draft when `pr.draft` is true.
5. Run the command a second time. It prints `exists <url>` and does nothing else, because the pull request is
   recorded. Run `ralph pr unlink --milestone M-1`, then `ralph pr create --milestone M-1` again: it prints
   `adopted <url>` for the same pull request, since it is still open.
6. Decline the scratch pull request in Bitbucket.
