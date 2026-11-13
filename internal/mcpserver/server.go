// Package mcpserver exposes ralph's service layer as MCP tools so an agent
// harness can read and write initiatives, documents, milestones, and tasks.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/drift"
	"github.com/bengearig/ralph/internal/looplock"
	"github.com/bengearig/ralph/internal/service"
	"github.com/bengearig/ralph/internal/store"
	"github.com/bengearig/ralph/internal/workflow"
)

// Name is the server name; Claude Code exposes its tools as mcp__ralph__*.
const Name = "ralph"

const instructions = `ralph stores the plan for work following the RALPH method:
Receive instructions → Articulate a solution → Loop over tasks → Pull request → Human review.

- An initiative holds one set of received instructions (its brief) plus its documents and milestones.
- Documents capture architecture, design, decisions, and reference material, project-wide or per initiative.
- A milestone is an ordered group of tasks and becomes one branch and one pull request.
- Receive plans at a high level: milestones and unplanned PROPOSED tasks (a title and maybe a summary).
  Articulate works out each task's details with the user and promotes it.
- Tasks start PROPOSED. Promote a task to TODO only when it has a description and acceptance criteria
  that a fresh agent with no other context could implement and verify.
- The loop works TODO tasks in position order. Use BLOCKED (with a reason) or PROPOSED to kick work back
  instead of guessing. IDs look like I-1, M-2, T-3, D-4.
- Search before writing new documents or tasks, to build on what already exists.
- Deleting is for mistakes such as duplicates and needs the user's agreement; CANCELLED is the normal way to drop work.
- Archived initiatives and documents are hidden and read-only; list them with include_archived.
- ralph's workflows (Receive, Articulate, Reconcile, Status, Search) are available as MCP prompts of the same names; run one when the user asks for that phase.`

// Options configures tool behavior that comes from ralph's config.
type Options struct {
	// ConfirmOutline is receive.confirm_outline: whether Receive asks before creating a new initiative's outline.
	ConfirmOutline bool
	// DriftInput builds the drift check input for a milestone. Nil disables milestone_drift and milestone_reconcile.
	// TaskManagerError is why the configured task manager is disabled, when it is; cards_sync reports it.
	TaskManagerError error
	DriftInput       func(ctx context.Context, milestoneID int64) (drift.Input, error)
	// ProjectRoot is the directory import_queue resolves paths against.
	ProjectRoot string
	// ImportMaxFileBytes is import.max_file_bytes.
	ImportMaxFileBytes int
	// Notice is appended to the server instructions when non-empty.
	Notice string
}

// New builds an MCP server over svc.
func New(svc *service.Service, version string, log *slog.Logger, opts Options) *mcp.Server {
	ins := instructions
	if opts.Notice != "" {
		ins += "\n\n" + opts.Notice
	}
	s := mcp.NewServer(&mcp.Implementation{Name: Name, Title: "RALPH workflow store", Version: version}, &mcp.ServerOptions{
		Instructions: ins,
		Logger:       log,
	})
	t := &tools{svc: svc, tmErr: opts.TaskManagerError, confirmOutline: opts.ConfirmOutline, newDriftInput: opts.DriftInput, projectRoot: opts.ProjectRoot, importMaxBytes: opts.ImportMaxFileBytes}
	t.register(s)
	registerPrompts(s)
	return s
}

func registerPrompts(s *mcp.Server) {
	for _, w := range workflow.List() {
		s.AddPrompt(&mcp.Prompt{
			Name:        w.Name,
			Title:       "RALPH " + strings.ToUpper(w.Name[:1]) + w.Name[1:],
			Description: w.Description,
			Arguments:   []*mcp.PromptArgument{{Name: w.Argument, Description: w.ArgumentDescription, Required: false}},
		}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			text, err := workflow.Render(w.Name, workflow.Data{Target: workflow.Neutral, Args: req.Params.Arguments[w.Argument]})
			if err != nil {
				return nil, err
			}
			return &mcp.GetPromptResult{
				Description: w.Description,
				Messages:    []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: text}}},
			}, nil
		})
	}
}

type tools struct {
	svc            *service.Service
	tmErr          error
	confirmOutline bool
	newDriftInput  func(ctx context.Context, milestoneID int64) (drift.Input, error)
	projectRoot    string
	importMaxBytes int
}

// toolError rewrites service errors into guidance an agent can act on.
func toolError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return fmt.Errorf("%v (find valid IDs with initiative_list, milestone_list, task_list, or doc_list)", err)
	case errors.Is(err, store.ErrConflict):
		return fmt.Errorf("%v (choose a different slug or omit it to derive one)", err)
	case errors.Is(err, service.ErrNeedsForce):
		return fmt.Errorf("%v (ask the user before retrying with force: true)", err)
	case errors.Is(err, service.ErrArchived):
		return fmt.Errorf("%v (unarchive it first if the user wants to change it)", err)
	case errors.Is(err, looplock.ErrLoopRunning):
		return fmt.Errorf("%v (wait for the loop to finish or stop it)", err)
	}
	return err
}

func parseOptionalID(k domain.Kind, s string) (int64, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	return domain.ParseID(k, s)
}

func boolPtr(b bool) *bool { return &b }

// All tools act only on the local project store.
func readOnly(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, OpenWorldHint: boolPtr(false)}
}

func write(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, DestructiveHint: boolPtr(false), OpenWorldHint: boolPtr(false)}
}

func destructive(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, DestructiveHint: boolPtr(true), OpenWorldHint: boolPtr(false)}
}

const deletePolicy = "Deletes are permanent. To drop work, use task_set_status with CANCELLED instead. " +
	"Delete only mistakes such as duplicates or wrongly created items, and only after the user has agreed. "

const archivePolicy = "Archiving is reversible, unlike deleting; prefer it for work that is finished or no longer relevant. "

const forcePolicy = "Never pass force without the user's explicit approval. "

func (t *tools) register(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "initiative_create", Annotations: write("Create initiative"),
		Description: "Record a newly received set of instructions as an initiative. Put the instructions verbatim in brief. " +
			"Takes an optional parent_card, the Jira key of an existing card to attach the initiative's card to. " +
			"The result's confirm_outline says whether Receive must ask the user before creating the outline."}, t.initiativeCreate)
	mcp.AddTool(s, &mcp.Tool{Name: "initiative_list", Annotations: readOnly("List initiatives"),
		Description: "List initiatives, each with its card when a task manager is configured. Archived ones are hidden unless include_archived is set."}, t.initiativeList)
	mcp.AddTool(s, &mcp.Tool{Name: "initiative_get", Annotations: readOnly("Get initiative"),
		Description: "Get an initiative with its brief and, when a task manager is configured, its card (key, URL, and pending when it is behind)."}, t.initiativeGet)
	mcp.AddTool(s, &mcp.Tool{Name: "initiative_update", Annotations: write("Update initiative"),
		Description: "Edit an initiative's title, brief, or parent card. Omitted fields are unchanged."}, t.initiativeUpdate)
	mcp.AddTool(s, &mcp.Tool{Name: "initiative_delete", Annotations: destructive("Delete initiative"),
		Description: deletePolicy + forcePolicy + "Cascades to the initiative's milestones, their tasks and notes, and its documents. " +
			"Refused without force when the initiative has milestones or documents, and while a loop is running on one of its milestones."}, t.initiativeDelete)

	archiveSchema, err := initiativeArchiveSchema()
	if err != nil {
		panic(err)
	}
	mcp.AddTool(s, &mcp.Tool{Name: "initiative_archive", Annotations: write("Archive initiative"), InputSchema: archiveSchema,
		Description: archivePolicy + "Archives the initiative: it and its milestones, tasks, and documents are hidden from lists, status, and search " +
			"and become read-only. Its initiative-scoped documents stay hidden unless promoted to project-wide: pass promote as an array of document IDs, " +
			"\"all\", or \"none\". Refused, changing nothing, when the initiative has such documents and no promote was given; the error lists them with a suggested selection " +
			"(design and decision documents), so ask the user, then rerun with promote. The result's promoted_documents lists what was promoted. " +
			"Refused while a loop is running on one of its milestones."}, t.initiativeArchive)
	mcp.AddTool(s, &mcp.Tool{Name: "initiative_unarchive", Annotations: write("Unarchive initiative"),
		Description: "Reverse initiative_archive, making the initiative and its contents visible and editable again. " +
			"Refused while a loop is running on one of its milestones."}, t.initiativeUnarchive)

	mcp.AddTool(s, &mcp.Tool{Name: "milestone_create", Annotations: write("Create milestone"),
		Description: "Add a milestone to an initiative. Each milestone becomes one branch and one pull request, so keep it independently reviewable. depends_on lists milestones (M-n, possibly in other initiatives) that must finish first; cycles are refused."}, t.milestoneCreate)
	mcp.AddTool(s, &mcp.Tool{Name: "milestone_list", Annotations: readOnly("List milestones"),
		Description: "List milestones with derived status and task counts, optionally for one initiative. Milestones of archived initiatives are hidden unless include_archived is set."}, t.milestoneList)
	mcp.AddTool(s, &mcp.Tool{Name: "milestone_get", Annotations: readOnly("Get milestone"),
		Description: "Get a milestone with its tasks in loop order and, when a task manager is configured, its card."}, t.milestoneGet)
	mcp.AddTool(s, &mcp.Tool{Name: "milestone_update", Annotations: write("Update milestone"),
		Description: "Edit a milestone's title, description, position, or dependencies. Omitted fields are unchanged. depends_on replaces the whole set (milestones may be in other initiatives; cycles are refused); an empty array clears it."}, t.milestoneUpdate)
	mcp.AddTool(s, &mcp.Tool{Name: "milestone_drift", Annotations: readOnly("Check milestone drift"),
		Description: "Compare a milestone's recorded git state with its branch and report drift per task. Read-only."}, t.milestoneDrift)
	mcp.AddTool(s, &mcp.Tool{Name: "milestone_reconcile", Annotations: destructive("Reconcile milestone drift"),
		Description: "Resolve a milestone's drift: point it at a branch, reopen DONE tasks whose work is gone (reason required), " +
			"and/or accept the current branch tip. Get the user's approval first."}, t.milestoneReconcile)
	mcp.AddTool(s, &mcp.Tool{Name: "milestone_delete", Annotations: destructive("Delete milestone"),
		Description: deletePolicy + forcePolicy + "Cascades to the milestone's tasks and their notes. " +
			"Refused without force when the milestone has tasks, and while a loop is running on it."}, t.milestoneDelete)

	mcp.AddTool(s, &mcp.Tool{Name: "task_create", Annotations: write("Propose task"),
		Description: "Propose a task in a milestone. It starts PROPOSED; refine it, then promote it to TODO with task_set_status."}, t.taskCreate)
	mcp.AddTool(s, &mcp.Tool{Name: "task_import", Annotations: write("Import task"),
		Description: "For importing existing task lists only, for example a tracker file during an import; for planning new work use task_create, which always starts PROPOSED. " +
			"Creates a task directly in PROPOSED, TODO, BLOCKED, DONE, or CANCELLED. Record why the status was chosen in reason, and prefer PROPOSED when unsure. " +
			"TODO needs a description and acceptance criteria; BLOCKED uses reason as its blocked reason. IN_PROGRESS is not allowed: import in-progress items as TODO or PROPOSED."}, t.taskImport)
	mcp.AddTool(s, &mcp.Tool{Name: "task_list", Annotations: readOnly("List tasks"),
		Description: "List tasks in loop order, filtered by milestone, initiative, or statuses. Tasks of archived initiatives are hidden unless include_archived is set."}, t.taskList)
	mcp.AddTool(s, &mcp.Tool{Name: "task_get", Annotations: readOnly("Get task"),
		Description: "Get a task with its notes, the statuses it may move to, and, when a task manager is configured, its card."}, t.taskGet)
	mcp.AddTool(s, &mcp.Tool{Name: "task_update", Annotations: write("Update task"),
		Description: "Edit a task's title, description, acceptance criteria, or position. Omitted fields are unchanged. DONE and CANCELLED tasks cannot be edited."}, t.taskUpdate)
	mcp.AddTool(s, &mcp.Tool{Name: "task_set_status", Annotations: write("Set task status"),
		Description: "Move a task to a new status. TODO requires a description and acceptance criteria; BLOCKED requires a reason. " +
			"A failed iteration (IN_PROGRESS to TODO) counts toward the stall threshold, after which the task is BLOCKED automatically."}, t.taskSetStatus)
	mcp.AddTool(s, &mcp.Tool{Name: "task_add_note", Annotations: write("Add task note"),
		Description: "Append a note to a task. Use ITERATION notes to leave context for the next loop iteration, KICKBACK to explain a return to articulation."}, t.taskAddNote)
	mcp.AddTool(s, &mcp.Tool{Name: "task_next", Annotations: readOnly("Next task"),
		Description: "Get the task the loop should work on next in a milestone: an interrupted IN_PROGRESS task, else the first TODO."}, t.taskNext)
	mcp.AddTool(s, &mcp.Tool{Name: "task_delete", Annotations: destructive("Delete task"),
		Description: deletePolicy + forcePolicy + "Cascades to the task's notes. " +
			"Refused without force when the task is IN_PROGRESS or DONE, and while a loop is running on its milestone."}, t.taskDelete)
	mcp.AddTool(s, &mcp.Tool{Name: "note_delete", Annotations: destructive("Delete task note"),
		Description: deletePolicy + "Takes a note ID such as N-3, shown by task_get. Removes only that note. " +
			"Refused while a loop is running on the note's milestone."}, t.noteDelete)

	mcp.AddTool(s, &mcp.Tool{Name: "doc_create", Annotations: write("Create document"),
		Description: "Create a markdown document. Omit initiative_id for project-wide documents such as overall architecture."}, t.docCreate)
	mcp.AddTool(s, &mcp.Tool{Name: "doc_list", Annotations: readOnly("List documents"),
		Description: "List documents (without bodies), optionally filtered by initiative, project scope, or kind. Archived documents and those of archived initiatives are hidden unless include_archived is set."}, t.docList)
	mcp.AddTool(s, &mcp.Tool{Name: "doc_get", Annotations: readOnly("Get document"),
		Description: "Get a document including its body."}, t.docGet)
	mcp.AddTool(s, &mcp.Tool{Name: "doc_update", Annotations: write("Update document"),
		Description: "Edit a document's kind, title, or body. Omitted fields are unchanged; body replaces the whole body."}, t.docUpdate)
	mcp.AddTool(s, &mcp.Tool{Name: "doc_archive", Annotations: write("Archive document"),
		Description: archivePolicy + "Archives the document: it is hidden from lists and search and becomes read-only."}, t.docArchive)
	mcp.AddTool(s, &mcp.Tool{Name: "doc_unarchive", Annotations: write("Unarchive document"),
		Description: "Reverse doc_archive. A document in an archived initiative stays hidden and read-only until the initiative is unarchived; the view's archived field says so."}, t.docUnarchive)
	mcp.AddTool(s, &mcp.Tool{Name: "doc_promote", Annotations: write("Promote document to project-wide"),
		Description: "Make an initiative's document project-wide, so it outlives the initiative and stays visible when the initiative is archived. " +
			"The view keeps origin_initiative_id as a link back. Refused if the document is already project-wide or is itself archived. " +
			"Refused when a project-wide document already has the slug, naming that document; retry with a new slug."}, t.docPromote)
	mcp.AddTool(s, &mcp.Tool{Name: "doc_return", Annotations: write("Return document to its initiative"),
		Description: "Reverse doc_promote: move a promoted document back to its origin initiative and clear the origin link. " +
			"Refused when the document has no origin, when the origin initiative or the document is archived, " +
			"or when a document in the origin initiative already has the slug, naming that document; retry with a new slug."}, t.docReturn)
	mcp.AddTool(s, &mcp.Tool{Name: "doc_delete", Annotations: destructive("Delete document"),
		Description: deletePolicy + "Removes only that document and its search index entries; nothing else cascades and nothing blocks it."}, t.docDelete)

	t.registerImports(s)

	mcp.AddTool(s, &mcp.Tool{Name: "search", Annotations: readOnly("Search"),
		Description: "Semantic search across initiatives, milestones, tasks, and documents. Falls back to keyword search when embeddings are unavailable; the result reports which mode ran. Archived items are excluded unless include_archived is set."}, t.search)
	mcp.AddTool(s, &mcp.Tool{Name: "cards_sync", Annotations: &mcp.ToolAnnotations{Title: "Sync cards", DestructiveHint: boolPtr(false), IdempotentHint: true, OpenWorldHint: boolPtr(true)},
		Description: "Create Jira (task manager) cards for existing work that has none and retry pending ones. It is explicit and never runs automatically; " +
			"call it only when the user asks. Cards are created only for the kinds in task_manager.card_kinds; existing cards are updated whatever their kind, and other resources are skipped. Nothing is created for cancelled work. Safe to rerun. Returns a report of created, updated, in_sync, skipped, and failed items. " +
			"Optionally scope to one initiative; archived initiatives are skipped unless include_archived is set."}, t.cardsSync)
	mcp.AddTool(s, &mcp.Tool{Name: "status", Annotations: readOnly("Status"),
		Description: "Summarize progress: per initiative, its milestones with derived status and task counts. Archived initiatives are hidden unless include_archived is set."}, t.status)
}
