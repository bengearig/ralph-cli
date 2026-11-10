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
- Deleting is for mistakes such as duplicates and needs the user's agreement; CANCELLED is the normal way to drop work.`

// Options configures tool behavior that comes from ralph's config.
type Options struct {
	// ConfirmOutline is receive.confirm_outline: whether Receive asks before creating a new initiative's outline.
	ConfirmOutline bool
	// DriftInput builds the drift check input for a milestone. Nil disables milestone_drift and milestone_reconcile.
	DriftInput func(ctx context.Context, milestoneID int64) (drift.Input, error)
}

// New builds an MCP server over svc.
func New(svc *service.Service, version string, log *slog.Logger, opts Options) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: Name, Title: "RALPH workflow store", Version: version}, &mcp.ServerOptions{
		Instructions: instructions,
		Logger:       log,
	})
	t := &tools{svc: svc, confirmOutline: opts.ConfirmOutline, newDriftInput: opts.DriftInput}
	t.register(s)
	return s
}

type tools struct {
	svc            *service.Service
	confirmOutline bool
	newDriftInput  func(ctx context.Context, milestoneID int64) (drift.Input, error)
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

const forcePolicy = "Never pass force without the user's explicit approval. "

func (t *tools) register(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "initiative_create", Annotations: write("Create initiative"),
		Description: "Record a newly received set of instructions as an initiative. Put the instructions verbatim in brief. " +
			"The result's confirm_outline says whether Receive must ask the user before creating the outline."}, t.initiativeCreate)
	mcp.AddTool(s, &mcp.Tool{Name: "initiative_list", Annotations: readOnly("List initiatives"),
		Description: "List all initiatives."}, t.initiativeList)
	mcp.AddTool(s, &mcp.Tool{Name: "initiative_get", Annotations: readOnly("Get initiative"),
		Description: "Get an initiative with its brief."}, t.initiativeGet)
	mcp.AddTool(s, &mcp.Tool{Name: "initiative_update", Annotations: write("Update initiative"),
		Description: "Edit an initiative's title or brief. Omitted fields are unchanged."}, t.initiativeUpdate)
	mcp.AddTool(s, &mcp.Tool{Name: "initiative_delete", Annotations: destructive("Delete initiative"),
		Description: deletePolicy + forcePolicy + "Cascades to the initiative's milestones, their tasks and notes, and its documents. " +
			"Refused without force when the initiative has milestones or documents, and while a loop is running on one of its milestones."}, t.initiativeDelete)

	mcp.AddTool(s, &mcp.Tool{Name: "milestone_create", Annotations: write("Create milestone"),
		Description: "Add a milestone to an initiative. Each milestone becomes one branch and one pull request, so keep it independently reviewable."}, t.milestoneCreate)
	mcp.AddTool(s, &mcp.Tool{Name: "milestone_list", Annotations: readOnly("List milestones"),
		Description: "List milestones with derived status and task counts, optionally for one initiative."}, t.milestoneList)
	mcp.AddTool(s, &mcp.Tool{Name: "milestone_get", Annotations: readOnly("Get milestone"),
		Description: "Get a milestone with its tasks in loop order."}, t.milestoneGet)
	mcp.AddTool(s, &mcp.Tool{Name: "milestone_update", Annotations: write("Update milestone"),
		Description: "Edit a milestone's title, description, or position. Omitted fields are unchanged."}, t.milestoneUpdate)
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
	mcp.AddTool(s, &mcp.Tool{Name: "task_list", Annotations: readOnly("List tasks"),
		Description: "List tasks in loop order, filtered by milestone, initiative, or statuses."}, t.taskList)
	mcp.AddTool(s, &mcp.Tool{Name: "task_get", Annotations: readOnly("Get task"),
		Description: "Get a task with its notes and the statuses it may move to."}, t.taskGet)
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
		Description: "List documents (without bodies), optionally filtered by initiative, project scope, or kind."}, t.docList)
	mcp.AddTool(s, &mcp.Tool{Name: "doc_get", Annotations: readOnly("Get document"),
		Description: "Get a document including its body."}, t.docGet)
	mcp.AddTool(s, &mcp.Tool{Name: "doc_update", Annotations: write("Update document"),
		Description: "Edit a document's kind, title, or body. Omitted fields are unchanged; body replaces the whole body."}, t.docUpdate)
	mcp.AddTool(s, &mcp.Tool{Name: "doc_delete", Annotations: destructive("Delete document"),
		Description: deletePolicy + "Removes only that document and its search index entries; nothing else cascades and nothing blocks it."}, t.docDelete)

	mcp.AddTool(s, &mcp.Tool{Name: "search", Annotations: readOnly("Search"),
		Description: "Semantic search across initiatives, milestones, tasks, and documents. Falls back to keyword search when embeddings are unavailable; the result reports which mode ran."}, t.search)
	mcp.AddTool(s, &mcp.Tool{Name: "status", Annotations: readOnly("Status"),
		Description: "Summarize progress: per initiative, its milestones with derived status and task counts."}, t.status)
}
