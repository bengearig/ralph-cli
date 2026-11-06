# 2. Go, with cobra for commands and koanf for layered configuration

- Status: Accepted
- Date: 2026-11-06

## Context

ralph must be written in Go and must be fully configurable: which harness runs each step, where pull requests go,
which embedding model is used, and the loop policy. Settings come from personal, team, and per-run sources.

## Decision

- Use `spf13/cobra` for the command tree.
- Use `knadh/koanf/v2` for configuration, merging built-in defaults, a global file, a project file, an optional
  extra file, `RALPH_*` environment variables (with `__` as the nesting separator), and flags, in that order.
- Use YAML for configuration files.
- Keep the built-in defaults in an embedded, commented `defaults.yaml`. It is the single source of truth for default
  values and for the project template that `ralph init` writes.
- Decode strictly, rejecting unknown keys, and validate every setting, reporting all problems at once.

koanf was chosen over viper because it is lighter, does not lowercase keys implicitly, and makes precedence
explicit.

## Consequences

- Typos in config files fail loudly instead of being silently ignored.
- Key names are snake_case, so every key can also be set through an environment variable.
- New settings need a default in `defaults.yaml`, a struct field, and validation.
