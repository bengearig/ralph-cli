# Glossary

These terms are used consistently in code, documentation, CLI output, MCP tool descriptions, and prompts.

**Acceptance criteria.** Verifiable conditions, ideally tests or commands, that show a task is done. A task needs
acceptance criteria before it can move to `TODO`.

**Archive.** Hiding an initiative or document, and everything under an initiative, from lists, status, and search, and
making it read-only, without deleting it. Archiving is reversible; see [Archiving](data-model.md#archiving).

**Articulate.** The second phase: low-level planning. Each unplanned task is worked out with a person until a
stranger, or a fresh agent, could implement and verify it from its description and acceptance criteria alone, and is
then promoted to `TODO`.

**Brief.** The instructions for an initiative exactly as they were received. Clarifications are stored separately as
`brief` or `decision` documents, so the original text is never lost.

**Card.** The record in an external task manager that corresponds to one initiative, milestone, or task. Ralph keeps
the card's key, URL, and last-pushed content hash in the `cards` table; see [Card records](data-model.md#card-records).

**Chunk.** A piece of an entity's text, sized for embedding. Search ranks chunks and reports each entity's
best-matching chunk.

**Display ID.** The human- and agent-facing form of an ID: a kind prefix and a number, such as `I-1`
(initiative), `M-2` (milestone), `T-3` (task), or `D-4` (document).

**Document.** Durable written knowledge: `brief`, `architecture`, `design`, `decision`, `reference`, or `note`. A
document belongs either to the whole project or to one initiative.

**Drift.** A difference between a milestone's branch and the git state the loop recorded for it: commits added on
top, commits lost, history rewritten by a rebase, amend or squash, a missing or renamed branch, or a branch already
merged. The loop and `ralph pr create` check for drift and refuse to go on until it is reconciled.

**Embedding.** A vector representing a chunk's meaning, produced by the configured model. Each stored vector records
its model identity (the model plus any document prefix), and vectors with different identities are never compared.

**Harness.** The agent runtime that ralph drives, such as Claude Code. It is configured per step.

**Import.** Bringing an existing text file into ralph's documents. The file is queued as an import record with a
snapshot of its content, integrated into documents by an agent (or imported verbatim with `--as-new`), and then
resolved. The agent workflow (`/ralph:import`, or the `import` MCP prompt) works one file at a time: it applies
additive changes directly and asks for approval before resolving contradictions, and a file whose contradictions stay
open is left with the status `deferred`, to be picked up again. After an import, ralph's documents are the source of
truth, not the file. See [Imports](data-model.md#imports).

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

**Reconcile.** Resolving drift with `ralph milestone reconcile`: reviewing where each `DONE` task's work is now on
the branch, then pointing the record at a renamed branch, reopening tasks whose work is gone, or accepting the branch
as the new record.

**Stale chunk.** A chunk without an embedding for the configured model identity. Stale chunks are found by keyword search
only, until `ralph index` embeds them.

**Stall.** A task whose failure count has reached `loop.stall_threshold`. A stalled task is blocked rather than
retried.

**Step.** A phase that can be configured with its own harness and model: `receive`, `articulate`, `loop`, or `pr`.

**Task.** A single loopable unit of work with a description, acceptance criteria, status, position, and notes.

**Task manager.** An external tracker, such as Jira, that ralph can keep cards in. It sits behind a provider-neutral
interface in `internal/taskmanager`, selected by `task_manager.provider`; see
[`task_manager`](configuration.md#task_manager).

**Task note.** A dated entry on a task. Notes are of kind `NOTE`, `ITERATION` (context for the next iteration), or
`KICKBACK`.

**Unplanned task.** A `PROPOSED` task created during Receive, with only a title and perhaps a one-sentence summary.
Articulate works out its details.

**Watch.** The live form of a `show` or `list` command, such as `ralph initiative watch` or
`ralph task list --watch`. It polls the database, shows the view again whenever it changes, and never writes. See [Watching](cli.md#watching).
