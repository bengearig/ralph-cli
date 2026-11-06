# 9. One branch and one pull request per milestone; Bitbucket Cloud first

- Status: Accepted
- Date: 2026-11-06

## Context

Pull requests should be reviewable ("pull request, not pull rank"). One pull request per task produces many small
reviews; one per initiative produces a few very large ones. The first target host is Bitbucket Cloud.

## Decision

- Each milestone is delivered on its own branch, named by `loop.branch_template` (default
  `ralph/{{.Milestone.Slug}}`) and based on `pr.base_branch`, with an optional commit per task. It becomes exactly
  one pull request.
- Pull-request hosts implement a `pr.Provider` interface and register by name. The first is `bitbucket_cloud`
  (REST API 2.0). Its token is read from the environment variable named by `pr.bitbucket_cloud.token_env`.
- Pull requests open as drafts by default.

## Consequences

- Milestones must be scoped to be independently reviewable and to leave the code working.
- Other hosts can be added without touching the loop.
- PR creation is stubbed in the skeleton; `--dry-run` shows the request and validates the configuration.
