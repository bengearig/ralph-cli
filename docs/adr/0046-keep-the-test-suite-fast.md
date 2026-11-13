# 46. Keep the test suite fast

- Status: Accepted
- Date: 2026-11-13

## Context

The whole suite took about 4m40s, which made it too slow to run before each commit and slowed the loop, whose agent runs
the tests to check its work. Profiling showed little real work. Most of the time was waiting, or spawning `git`:

- Every watch test in `internal/cli` waited out a five second cleanup limit. The cleanup waited on a channel whose single
  value the test had already read, then gave up silently.
- The Jira card tests pointed the provider at `127.0.0.1:1`, expecting an instant refusal. On some machines that port
  holds the connection, so every command that called the provider waited for the default ten second
  `task_manager.timeout`.
- The `loop`, `drift`, and `git` packages use about a quarter of one core. They spend their time spawning `git`, and
  they ran no test in parallel because each one called `t.Setenv` for the home directory and git identity.

## Decision

- **Remove the waits.** Watch helpers signal completion by closing a channel and keep the result, so reading it twice is
  safe and the cleanup returns at once; it now reports an error if the watch does not stop. The Jira card tests use a
  local server that answers with an error and a two second timeout as a backstop, and one test keeps a stalling server
  to cover the configured timeout.
- **Parallelize the `git`-bound packages.** `loop`, `drift`, and `git` set the shared environment once in `TestMain`,
  and independent tests call `t.Parallel()`. Tests that change other environment stay serial.
- **Do less `git` work in test helpers.** Repository states that many tests rebuild are built once per process and
  restored, and helpers use one command where several were used.
- **Make slowness visible.** `scripts/slowest-tests.sh` lists the slowest packages and tests, and
  [Testing](../testing.md) records the conventions and how to profile the suite.
- **Guidance for the agent.** The loop prompt and the Articulate workflow carry matching guidance about fast checks; see
  [the loop](../loop.md) and [Claude Code integration](../claude-code.md).

Measured on 2026-11-13 on one developer machine, whole-suite runs with `go test -count=1`:

| | Before | After |
| --- | --- | --- |
| Whole suite, wall time | about 4m40s | about 35s |
| `internal/cli` | 285s | 36s |
| `internal/loop` | 21s | 3.6s |

The after figures are from `scripts/slowest-tests.sh` over the whole suite, so packages ran alongside each other. Run
alone with `-p 1`, `internal/loop` took 3.2s, `internal/drift` 5.7s before and 1.4s after, and `internal/git` 2.8s before
and 0.47s after. These are a snapshot of one moment and machine, not a target.

## Alternatives considered

- **Lower `task_manager.timeout` for everyone.** Rejected: it changes behavior users see, and the tests only needed their
  own setting.
- **Run tests in parallel inside `internal/cli`.** Rejected for now: `Execute` reads the process environment, so tests
  would have to share it. It needs the environment removed from `Execute` first, and the package is already fast.
- **Cancel the SQLite migration speedup.** Considered because its saving is small, and rejected: it stays, as it costs nothing to keep.

## Consequences

- The suite is quick enough to run in full before each commit, and the agent's checks in the loop cost less.
- Tests carry conventions that a new test can break without failing: a sleep, a real network call, or a `t.Setenv` in a
  package that expects parallel tests. [Testing](../testing.md) states them, and `scripts/slowest-tests.sh` shows a
  regression.
- Parallel tests can expose shared state that serial runs hid, so changes to those packages should be run with `-race`
  and `-shuffle=on`.
- The exact timings above age. Re-measure with `scripts/slowest-tests.sh` instead of trusting them.
