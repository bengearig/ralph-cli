# 34. Record pull requests without URLs

- Status: Accepted
- Date: 2026-11-11
- Refines: [16. Record the loop's git state](0016-record-the-loops-git-state.md)

## Context

A milestone becomes one pull request. Without a record of it, a repeated `ralph pr create` could not tell that a pull
request already exists. A stored URL would go stale when the hosting provider or its URL scheme changes.

## Decision

- A milestone records its pull request as the provider name, namespace, repository, and number. There is no URL and no
  other field. A record exists when the number is greater than zero.
- Links are built when shown, by a static function registered per provider next to its factory. The function needs no
  configuration, token, or network, so a record keeps its link after `pr.provider` changes.
- The service cannot import the provider package, so the CLI injects the link lookup into the service. A provider
  without a registered function, or no lookup at all, yields a view without a URL, and the CLI shows
  `<provider> <namespace>/<repository>#<number>` instead.
- Like the git records, writing the record does not change `updated_at`, reindex, take the loop guard, or push to the
  task manager. It is refused for a milestone in an archived initiative.
- `ralph pr unlink` clears the record so a new pull request can be opened, for example after one was declined. It is
  CLI-only and changes nothing on the hosting service.
- The record is not checked against the hosting service: nothing verifies that the pull request is still open.
- `ralph pr create` records the pull request once the provider confirms it; see the decisions on pushing, adopting,
  and titles below.
- The Bitbucket Cloud provider authenticates with `basic` by default: the Atlassian account email and an API token,
  because Bitbucket app passwords no longer work. `bearer` sends the token alone, for repository or workspace access
  tokens. Environment variable names are configuration; the credentials are read when a request is made and never
  stored.
- Adopting an existing pull request requires an open pull request whose source is the milestone's branch and whose
  destination equals `pr.base_branch`. A pull request from the same branch into another branch is not adopted, so the
  pull request the milestone needs can still be opened. Among several matches the most recently updated is used.
- The provider interface gains `FindOpen`, and a `PullRequest` carries namespace, repository, and number, so the
  command can record it without asking the provider's configuration. Every call is a single attempt bounded by
  `pr.timeout`.

- `ralph pr create` pushes the milestone's branch to `pr.remote` (default `origin`) with a plain, tracked push of
  that branch only. The push is never forced, with neither `--force`, `--force-with-lease`, nor a `+` refspec, and it
  works whichever branch is checked out. A missing remote, a rejected push, or an authentication failure exits with
  code 3 and opens nothing. It is the only command that pushes: `ralph milestone merge` and the loop still push
  nothing, which clarifies [ADR 27](0027-merge-milestones-locally-with-fast-forward-only.md) and
  [ADR 29](0029-looping-an-initiative.md), whose statements about pushing concern merging.
- The flow is: stop with `exists` when a pull request is already recorded, otherwise push, then adopt an open pull
  request from the branch into `pr.base_branch` if the provider has one, otherwise create one. The result is recorded
  only after the provider confirms it. If recording fails, the link is shown with an error and rerunning adopts the
  pull request; rerunning after any failure is safe because pushing again is a no-op and the provider is asked first.
- When a task manager is configured and the milestone's card has a key, the title is prefixed with it, as in
  `[KEY] M-n: Title`, in the style of commit subjects. Only the recorded card is read, so there is no provider call
  and no warning when the key is missing.
- After `ralph pr create` reaches `opened`, `adopted`, or `exists`, it links the pull request on the milestone's card
  through the optional `PullRequestLinker` task manager interface. The link has a stable identifier, so it is
  idempotent and rerunning the command retries a failed link. A failure is a warning (and `card_warning` with
  `--json`) and never changes the outcome or exit code. Only the milestone's card is linked, `--dry-run` links
  nothing, and `ralph pr unlink` removes no link. See [Pull request links](../jira.md#pull-request-links).
- `--dry-run` pushes nothing and calls no provider API. It prints the remote and branch that would be pushed, and the
  recorded pull request when there is one.
- The GitHub provider targets github.com only, at its public API; GitHub Enterprise Server is out of scope. It uses the
  REST API with a token sent as a bearer credential, a personal access token or an installation token, read from the
  variable named by `pr.github.token_env`. `RALPH_GITHUB_TOKEN` is only that setting's default.
- The GitHub provider keeps the neutral record: owner as the namespace, the repository, and the pull request number,
  with no URL. Its link function is registered next to its factory, and adopting uses the same rule as above, with the
  head and base branches in place of source and destination.
- `pr.draft: true` is sent as is. A repository without draft pull requests makes GitHub refuse the request, and the
  error suggests `pr.draft: false` rather than ralph retrying without the draft flag.

## Consequences

- Links survive a provider change and can be corrected in one place.
- A declined pull request keeps being reported until someone unlinks it.
- Merging and the loop remain free of network access; the push happens only when a person asks for the pull request.
- A branch rejected by the remote needs a person to decide how to reconcile it; ralph never overwrites remote history.
- Adding a provider means registering a link function alongside its factory, as the GitHub provider does.
