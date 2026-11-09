# Glossary

These terms are used consistently in code, documentation, CLI output, MCP tool descriptions, and prompts.

**Acceptance criteria.** Verifiable conditions, ideally tests or commands, that show a task is done. A task needs
acceptance criteria before it can move to `TODO`.

**Articulate.** The second phase: low-level planning. Each unplanned task is worked out with a person until a
stranger, or a fresh agent, could implement and verify it from its description and acceptance criteria alone, and is
then promoted to `TODO`.

**Brief.** The instructions for an initiative exactly as they were received. Clarifications are stored separately as
`brief` or `decision` documents, so the original text is never lost.

**Chunk.** A piece of an entity's text, sized for embedding. Search ranks chunks and reports each entity's
best-matching chunk.

**Display ID.** The human- and agent-facing form of an ID: a kind prefix and a number, such as `I-1`
(initiative), `M-2` (milestone), `T-3` (task), or `D-4` (document).

**Document.** Durable written knowledge: `brief`, `architecture`, `design`, `decision`, `reference`, or `note`. A
document belongs either to the whole project or to one initiative.

**Embedding.** A vector representing a chunk's meaning, produced by the configured model. Each stored vector records
its model identity (the model plus any document prefix), and vectors with different identities are never compared.

**Harness.** The agent runtime that ralph drives, such as Claude Code. It is configured per step.

**Initiative.** One received set of instructions and everything articulated from it.

**Iteration.** One fresh, headless harness session working on one task during the loop.

**Kickback.** Sending work back to an earlier phase: a task back to `PROPOSED` for re-articulation, or to `BLOCKED`
for human help. Kickbacks are recorded as `KICKBACK` notes.

**Loop.** The third phase. The agent works a milestone's `TODO` tasks one iteration at a time.

**Milestone.** An ordered group of tasks within an initiative. Each milestone becomes one branch and one pull
request. Its status is derived from its tasks.

**Project.** A repository initialized with `ralph init`, identified by its `.ralph/` directory.

**Receive.** The first phase: high-level planning. The instructions are recorded verbatim as an initiative's brief,
high-level questions are resolved, and the work is outlined as milestones and unplanned tasks.

**Stale chunk.** A chunk without an embedding for the configured model identity. Stale chunks are found by keyword search
only, until `ralph index` embeds them.

**Stall.** A task whose failure count has reached `loop.stall_threshold`. A stalled task is blocked rather than
retried.

**Step.** A phase that can be configured with its own harness and model: `receive`, `articulate`, `loop`, or `pr`.

**Task.** A single loopable unit of work with a description, acceptance criteria, status, position, and notes.

**Task note.** A dated entry on a task. Notes are of kind `NOTE`, `ITERATION` (context for the next iteration), or
`KICKBACK`.

**Unplanned task.** A `PROPOSED` task created during Receive, with only a title and perhaps a one-sentence summary.
Articulate works out its details.
