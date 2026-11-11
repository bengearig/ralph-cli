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
- `ralph pr create` does not record anything yet; recording comes with its implementation.

## Consequences

- Links survive a provider change and can be corrected in one place.
- A declined pull request keeps being reported until someone unlinks it.
- Adding a provider means registering a link function alongside its factory.
