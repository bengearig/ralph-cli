# Roadmap

This page tracks what the skeleton deliberately leaves stubbed, and the open questions behind each item.

## Stubbed in the skeleton

| Capability | Today | Next |
|---|---|---|
| Pull requests | `ralph pr create` opens the pull request on Bitbucket Cloud for a branch that is already pushed, and `--dry-run` prints the request and validates the configuration. The provider can find an open pull request, but the command does not use it yet. | Push the branch, adopt an existing open pull request, and record the pull request on the milestone (milestone M-32). |

## Open questions

- **Per-step harness for interactive steps.** `steps.receive` and `steps.articulate` are configurable but have no
  effect yet, because those phases run in the user's own session. Decide whether `ralph receive` and
  `ralph articulate` should launch the configured harness.
- **Plan reviewability.** The database is git-ignored, so reviewers do not see the plan in the pull request. Consider
  rendering the milestone's tasks and the relevant documents into the PR description, or adding a `ralph export`
  command.

## Future work

- More harnesses, behind the existing `harness` registry.
- More PR providers, such as Bitbucket Data Center, GitHub, GitLab, or Azure DevOps, behind the `pr` registry.
- More embedding providers, such as Voyage AI or OpenAI-compatible APIs, behind `embed.Embedder`.
- Task dependencies beyond position ordering.
- `ralph doctor` to check git, the harness binary, Ollama and the model, MCP registration, and provider credentials
  in one place.
- Developer tooling (Makefile, linters, CI, release builds) once the skeleton settles.
