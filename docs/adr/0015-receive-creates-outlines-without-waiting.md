# 15. Receive creates outlines without waiting

- Status: Accepted
- Date: 2026-11-09
- Refines: [14. Receive plans at a high level, Articulate at a low level](0014-receive-and-articulate-responsibilities.md)

## Context

Under ADR 14, Receive created the outline of milestones and unplanned tasks only once the person agreed to it, and
it asked for that agreement in an open-ended message. When the person moved on without answering, for example to
`/ralph:articulate`, nothing had been created: the outline existed only in the conversation.

## Decision

- **New initiatives create the outline right away.** After the high-level questions, Receive creates the milestones
  and their unplanned tasks without waiting, then shows the outline with its new IDs and invites changes. Changes
  asked for afterwards are made in place.
- **Approval goes through the harness's plan mode.** When approval is needed, Receive presents the outline as a plan
  in the harness's plan mode (in Claude Code, `EnterPlanMode` and `ExitPlanMode`) and creates or applies it only
  once the plan is approved. If the user rejects the plan with feedback, Receive revises it and presents it again.
- **When approval is needed.** When the user asks for it in the conversation, when `receive.confirm_outline: true`
  is set, and always when revising an initiative with `/ralph:receive I-n`.
- **Already in plan mode.** If the session is in plan mode when Receive starts, write tools are unavailable. Receive
  does the read-only work and puts the brief, the decisions, and the outline in the plan. After approval, it records
  the initiative, its documents, and the outline, in that order.
- **Declining plan mode.** If the user declines plan mode or does not approve the plan, nothing is created.
- **Harnesses without plan mode are deferred.** The approach for them will be decided when one is supported.
- **The setting is a top-level `receive.confirm_outline`**, a boolean defaulting to `false`, beside `loop` and `pr`.
  It is not `steps.receive.*`, because `steps` holds only harness and model overrides.
- **The agent learns the setting from the `initiative_create` MCP result**, which carries a top-level
  `confirm_outline` beside the initiative's fields. It is not rendered into the template at `ralph init` or read with
  a shell call, so the slash-command template stays static and shipped-digest updates keep working. The value is
  read when `ralph mcp` starts, like every other setting, so a change takes effect in the next session.

## Consequences

- The outline can no longer be lost to an unanswered question.
- Approval, when wanted, is explicit and uses the harness's own approval UI rather than a free-form reply.
- Revisions stay gated, because they can cancel tasks, which is terminal, and demote `TODO` tasks.
- The `initiative_create` MCP result differs from `ralph --json initiative create` by the `confirm_outline` field.
