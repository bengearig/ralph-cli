# Testing

The suite is meant to stay fast enough to run before every commit. This page covers how to run it, how to see where the
time goes, and the conventions that keep it quick. [ADR 46](adr/0046-keep-the-test-suite-fast.md) records why, with
measurements.

## Running tests

```sh
go test ./...                                # the whole suite
go test ./internal/loop                      # one package
go test ./internal/loop -run TestRunCommit   # tests whose name matches a regular expression
go test -race ./...                          # with the race detector
```

Add `-count=1` to bypass the test cache, and `-v` to see each test's time. The race detector is several times slower,
so use it on the packages you changed while iterating and on everything before a pull request.

## Seeing where the time goes

`scripts/slowest-tests.sh` runs `go test -count=1 -json` and prints the packages by elapsed time, then the slowest
top-level tests. It needs `jq`, takes `-n COUNT` (default 15) and optional package arguments (default `./...`), and
exits with the status of `go test`.

```sh
scripts/slowest-tests.sh -n 10 ./internal/cli
```

What to look for:

- A test that takes about as long as a round number of seconds (a default timeout, a sleep) is waiting, not working.
- Compare wall time with CPU time, for example with `time go test -count=1 ./internal/loop`. When the wall time is far
  above the user plus system time, the package is waiting on something such as a timeout or a sleep. When both are
  close to the wall time, it is doing real work, and the fix is to do less of it.
- A package whose wall time is high but whose CPU use is a fraction of one core is usually bound by spawning
  processes such as `git`, and benefits from parallel tests.
- Packages run in parallel with each other, so a package's elapsed time in a whole-suite run is longer than when it is
  run alone. Compare like with like.

## Conventions

- **No real network.** Use `httptest` or a fake provider. Never point a test at a port that may hang instead of
  refusing the connection: a closed local port is not guaranteed to refuse, and on some machines it holds the
  connection until the default `task_manager.timeout` expires, which made tests that only needed a failure wait for
  that timeout. Use a local server that answers with an error.
- **No fixed `time.Sleep` to wait for something.** Poll for the condition with a deadline, as `waitFor` does in
  `internal/cli/initiative_watch_test.go`. A fixed sleep is either too long on every run or too short on a slow one.
- **A helper that waits for completion must be safe to call after the result was already read.** Signal completion by
  closing a channel and keep the result in a field, rather than sending one value that the first reader consumes. The
  watch-test helper once waited on a channel that a test had already drained, so its cleanup always waited out its
  full limit and gave up silently. Have such a cleanup report an error when the wait times out.
- **Share environment in `TestMain`, and call `t.Parallel()` in independent tests.** `t.Setenv` cannot be used in a
  parallel test, so settings every test needs, such as the home directory and the git author and committer identity,
  are set once in the package's `TestMain`. A test that must change other environment, such as `PATH` or
  `GIT_CEILING_DIRECTORIES`, keeps `t.Setenv` and stays serial. Every other test uses its own `t.TempDir()`, shares no
  mutable package state, and calls `t.Parallel()` first. Run `go test -race -shuffle=on -count=3` on a package after
  making its tests parallel to expose hidden sharing.
- **Keep `git` calls in helpers to what the assertion needs.** These packages spend their time spawning `git`, so a
  helper that runs several commands where one would do, or re-reads a ref the test already knows, multiplies across
  every test that uses it. State that many tests rebuild identically can be built once per process and copied, provided
  it holds no absolute paths.
- **Keep a test that asserts a timeout on its own short timeout.** Other tests set a short timeout only as a backstop,
  so one test sets a small timeout against a server that stalls and asserts the command returns promptly. That keeps
  the configured timeout covered without every test paying for it.

The prompt the loop gives the agent and the Articulate workflow carry matching guidance about fast checks; see
[the loop](loop.md) and [Claude Code integration](claude-code.md).
