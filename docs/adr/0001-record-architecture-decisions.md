# 1. Record architecture decisions

- Status: Accepted
- Date: 2026-11-06

## Context

ralph is being designed iteratively, together with an agent. Decisions made in conversation are easily lost, and the
manifesto values "written plans over remembered intentions".

## Decision

Record significant decisions as numbered architecture decision records in `docs/adr/`, using Michael Nygard's
format: Context, Decision, Consequences. Accepted records are not rewritten. A changed decision gets a new record,
and the old record is marked `Superseded by N`.

## Consequences

- New contributors, human or agent, can learn why the system is shaped the way it is.
- Each significant change needs a short extra write-up.
