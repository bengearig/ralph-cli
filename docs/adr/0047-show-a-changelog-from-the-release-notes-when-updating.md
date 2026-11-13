# 47. Show a changelog from the release notes when updating

- Status: Accepted
- Date: 2026-11-13
- Refines: [38. Install from GitHub releases and update in place](0038-install-from-github-releases-and-update-in-place.md)

## Context

`ralph update` said which version it was moving to and where it installed it, but not what the new version brought. The
release workflow publishes each release with GitHub's generated notes, which list one line per merged pull request, and
ralph opens one pull request per milestone, so those lines are already a milestone-level history. The release client
already reads the release list from the GitHub API, and the API returns each release's body.

## Decision

- **The source is GitHub's generated release notes.** The command reads the body of each release through the release
  API it already uses. Nothing changes in the release workflow, the pull request titles, or ralph's store, which is local
  to a repository and not available to someone who installed a release.
- **The level is the milestone.** Ralph opens one pull request per milestone, so each line in the notes is a
  milestone's title.
- **It is condensed per version.** The command lists every release between the running one and the new one, oldest
  first, one block per version. Only each line's title is kept: author handles, pull request links, the
  new-contributors section, and the full-changelog link are removed. A release with no recognisable lines shows
  `no release notes`, so the range of versions stays visible.
- **It appears after the install and on `--check`.** `--check` shows what an update would bring and still changes
  nothing. With `--json` the same information is a `changelog` array. Downgrades, reinstalls, and an up-to-date copy show
  none; a build that is not a release build shows only the new release's notes.
- **Titles are shown as published.** The card key and milestone ID in a pull request title stay in the line.
- **A failure never blocks the update.** If the notes cannot be read, the update still installs and prints one warning
  line, or sets `changelog_error` with `--json`.

### Alternatives not chosen

- Grouping by initiative. It needs the release workflow and the pull request titles to change to carry an initiative.
- A hand-written `CHANGELOG.md`. It needs someone to write it for every release, and it can fall behind the releases.
- Showing the raw release bodies. They carry URLs, author handles, and sections that mean nothing in a terminal.
- Stripping the card key and milestone ID from each line. The user declined: the lines are shown as published.

## Consequences

- The changelog is only as good as the pull request titles; a vague title is a vague line.
- The card key and milestone ID appear in it, which is noise for someone without access to the task manager.
- Only the first page of releases is read, so an installed version older than that page shows the releases on it.
- The command makes one more request to GitHub on an update and on `--check`, and counts against the anonymous rate
  limit.
