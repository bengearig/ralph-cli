# GitHub

The GitHub provider lets `ralph pr create` open the pull request for a milestone's branch on **github.com**, through
the REST API with a token. GitHub Enterprise Server is not supported, and neither are GitHub Apps acting as
themselves or the `gh` CLI; a token is the only credential ralph uses.

Each call to GitHub is a single attempt. The provider never retries; a failure is returned with a hint about what to
check (see [Errors](#errors)).

See [Configuration](configuration.md#pr) for the settings, [`ralph pr create`](cli.md#loop-and-pull-requests) for the
command, [Bitbucket Cloud](bitbucket-cloud.md) for the other provider, and
[ADR 34](adr/0034-record-pull-requests-without-urls.md) for the decisions behind both.

## Setup

1. Create a token (see [The token](#the-token)).
2. Export it. The variable name is the one configured in `pr.github.token_env`; the default is `RALPH_GITHUB_TOKEN`:

   ```sh
   export RALPH_GITHUB_TOKEN="<token>"
   ```

3. Select the provider and set the repository's owner (a user or an organization) and name. Set `pr.base_branch` if the
   pull request should not target `main`:

   ```yaml
   # .ralph/config.yaml
   pr:
     provider: github
     base_branch: develop
     github:
       owner: acme
       repo: storefront
   ```

4. Validate the configuration:

   ```sh
   ralph config validate
   ```

   Validation makes no network call, so it cannot tell whether the token works. It reports every missing setting at
   once: the owner, the repository, the token variable name, and a token variable that is not set or is blank. Each
   message names the setting.

The settings and their defaults are listed under [`pr`](configuration.md#pr) in the configuration reference.

## The token

The configuration holds only the **name** of the environment variable, in `pr.github.token_env`. To keep the token in
a variable of another name, set `token_env` to that name; `RALPH_GITHUB_TOKEN` is only the default. The token is read
when a request is made and is never written to a config file, the store, or an error message. ralph sends it as a
bearer token.

Any of these works, as long as it can read and create pull requests in the repository:

- a fine-grained personal access token,
- a classic personal access token,
- an installation access token of a GitHub App, for automation that should not act as a person.

### Permissions

For a fine-grained personal access token, GitHub's documentation of
[permissions required for fine-grained personal access tokens](https://docs.github.com/en/rest/authentication/permissions-required-for-fine-grained-personal-access-tokens)
lists the repository permission **Pull requests** for both calls ralph makes: **write** access to create a pull
request and **read** access to list pull requests. Write access includes read. Grant the token access to the repository
you configured. The same page names no further permission for these two calls.

**What was not confirmed:** the scope a classic personal access token needs. GitHub's page for these endpoints does not
name classic scopes. The `repo` scope is the usual one for private repositories and `public_repo` for public ones, but
that was not checked against current documentation. An installation token's permission is also expected to be
**Pull requests** write, which was not separately confirmed either. If a call is refused with `403`, grant the missing
access and try again.

If the repository belongs to an organization that enforces SAML single sign-on, a classic token must also be authorized
for that organization.

## What ralph sends

`ralph pr create` first checks that the milestone's branch matches its recorded state and that the milestones it depends
on are merged, then pushes the branch to `pr.remote`; see [`ralph pr create`](cli.md#ralph-pr-create).

- **Finding.** A `GET` of the repository's pull requests, limited to open ones whose head is the milestone branch
  **and whose base is `pr.base_branch`**, most recently updated first. Only the first result is read. Adopting an
  existing pull request therefore requires the base to equal `pr.base_branch`: an open pull request from the same
  branch into a different branch is not adopted, so the pull request the milestone needs can still be opened.
- **Creating.** A `POST` to the repository's pull requests with the title, the body (the request body), the head
  branch, the base branch, and whether it is a draft. The response gives the pull request's number and web link.
- **Headers.** `Authorization` as a bearer token, `Accept` for the GitHub JSON media type, the REST API version,
  `Content-Type` as JSON when there is a body, and a `User-Agent` naming ralph and its version, which GitHub requires.

Each request is bounded by `pr.timeout`. Responses are capped in size, and a body quoted in an error is cut short.
Reviewers, labels, assignees, auto-merge, updating or merging a pull request, further result pages, and GitHub
Enterprise Server are not handled.

### Draft pull requests

`pr.draft` defaults to `true`, so ralph asks for a draft. GitHub offers draft pull requests in public repositories on
every plan, and in private repositories only on some paid plans. On a repository without them GitHub refuses the
request with a validation error that says draft pull requests are not supported, and ralph adds the hint
`set pr.draft: false`. Set `pr.draft: false` in the configuration, or move the repository to a plan that has them.

### The recorded pull request and the Jira link

The pull request is recorded on the milestone as the provider name, the owner, the repository, and the number, with no
URL, exactly as for Bitbucket Cloud. The link, `https://github.com/<owner>/<repo>/pull/<number>`, is built when shown by
a function that needs no configuration, token, or network, so the record keeps its link after `pr.provider` changes.
Afterwards the pull request is linked on the milestone's Jira card as for any provider; see
[Pull request links](jira.md#pull-request-links) and `ralph pr unlink` under [`ralph pr create`](cli.md#ralph-pr-create).

## Errors

A failed call returns an error that begins `github: <method> <path>:` and names the cause.

| Status | Message | What to check |
|---|---|---|
| 401 | `bad credentials (401)`; names `pr.github.token_env`. | The token variable, and that the token has not expired or been revoked. |
| 403 | `permission denied (403)`; the token lacks pull request write access, followed by GitHub's message. | The token's [permissions](#permissions), and organization restrictions such as SAML single sign-on. |
| 403 or 429 | `rate limited`, with the time the limit resets when GitHub sends it, and `Retry-After` on a 429. | Wait until the reset and run again. |
| 404 | `repository <owner>/<repo> not found or the token cannot see it (404)`. | `pr.github.owner` and `repo`, and that the token has access to the repository. GitHub answers 404 for a private repository the token cannot see. |
| 422 | `validation failed (422)` with GitHub's message and field errors, then the usual causes: a head branch that is not on the remote, no commits between the branches, or an existing pull request. | That the push to `pr.remote` reached the configured repository, that the branch has commits beyond the base, and that no open pull request exists. With a draft refusal, the added hint is `set pr.draft: false`. |
| other | The status code and text, with GitHub's message when there is one. | The message. |
| none | A timeout or network failure, wrapped with the method and path. | The network and `pr.timeout`. |

A missing token is reported before any request: `pr.github.token_env: environment variable <NAME> is not set or is
blank`. Errors never contain the token or the `Authorization` header.

## Manual check against a real repository

Automated tests use a fake server, so check once against a real repository before relying on the provider. Use a
scratch repository, because the last steps open a pull request.

1. Export the token and set `pr.provider`, `owner`, `repo`, and `pr.base_branch`. Run `ralph config validate`.
2. Check that `pr.remote` names the remote of the repository you configured. `ralph pr create` pushes the branch there.
3. Print what would be sent:

   ```sh
   ralph pr create --milestone M-1 --dry-run
   ```

   It prints the provider, the title, the branches, the drift check's result, the push that would happen, and the body.
   A configuration problem, such as an unset token variable, is printed as a warning. The dry run pushes nothing,
   sends nothing, and needs no network.
4. Open the pull request for real:

   ```sh
   ralph pr create --milestone M-1
   ```

   It pushes the branch and prints `opened <url>` on success. Open the link and confirm the title, the description,
   the head and base branches, and that the pull request is a draft when `pr.draft` is true.
5. Run the command a second time. It prints `exists <url>` and does nothing else, because the pull request is
   recorded. Run `ralph pr unlink --milestone M-1`, then `ralph pr create --milestone M-1` again: it prints
   `adopted <url>` for the same pull request, since it is still open.
6. Close the scratch pull request on GitHub.
