package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/drift"
	"github.com/bengearig/ralph/internal/loop"
	"github.com/bengearig/ralph/internal/service"
)

type empty struct{}

// ---- initiatives ----

type initiativeCreateIn struct {
	Title      string   `json:"title" jsonschema:"short title for the initiative"`
	Brief      string   `json:"brief,omitempty" jsonschema:"the instructions exactly as received"`
	Slug       string   `json:"slug,omitempty" jsonschema:"optional slug; derived from title when omitted"`
	DependsOn  []string `json:"depends_on,omitempty" jsonschema:"IDs of milestones this one depends on, for example M-15; may be in other initiatives; cycles are refused"`
	ParentCard string   `json:"parent_card,omitempty" jsonschema:"Jira key of an existing card to attach this initiative's card to, for example PROJ-123; set it only when the user names a parent card, otherwise leave it out"`
}

// initiativeCreateOut adds MCP-only guidance to the shared view (ADR 10).
type initiativeCreateOut struct {
	service.InitiativeView
	ConfirmOutline bool `json:"confirm_outline" jsonschema:"true: ask the user to approve the outline before creating milestones and tasks; false: show the outline and create it right away"`
}

func (t *tools) initiativeCreate(ctx context.Context, _ *mcp.CallToolRequest, in initiativeCreateIn) (*mcp.CallToolResult, initiativeCreateOut, error) {
	v, err := t.svc.CreateInitiative(ctx, service.InitiativeInput{Title: in.Title, Brief: in.Brief, Slug: in.Slug, ParentCard: in.ParentCard})
	if err != nil {
		return nil, initiativeCreateOut{}, toolError(err)
	}
	return nil, initiativeCreateOut{InitiativeView: v, ConfirmOutline: t.confirmOutline}, nil
}

type initiativeListIn struct {
	IncludeArchived bool `json:"include_archived,omitempty" jsonschema:"also include archived initiatives and documents, and their contents"`
}

type initiativeList struct {
	Initiatives []service.InitiativeView `json:"initiatives"`
}

func (t *tools) initiativeList(ctx context.Context, _ *mcp.CallToolRequest, in initiativeListIn) (*mcp.CallToolResult, initiativeList, error) {
	all, err := t.svc.ListInitiatives(ctx, service.ListOptions{IncludeArchived: in.IncludeArchived})
	return nil, initiativeList{Initiatives: all}, toolError(err)
}

// promoteChoice is the promote parameter: an array of document IDs, or "all" or "none".
type promoteChoice struct {
	items []string
}

func (p *promoteChoice) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		p.items = []string{s}
		return nil
	}
	var list []string
	if err := json.Unmarshal(b, &list); err != nil {
		return fmt.Errorf("%w: promote must be an array of document IDs, \"all\", or \"none\"", domain.ErrInvalid)
	}
	p.items = list
	return nil
}

func promoteSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Description: "which of the initiative's documents to promote to project-wide: an array of document IDs, or \"all\", or \"none\"",
		OneOf: []*jsonschema.Schema{
			{Type: "array", Items: &jsonschema.Schema{Type: "string"}},
			{Type: "string", Enum: []any{"all", "none"}},
		},
	}
}

type initiativeArchiveIn struct {
	ID      string        `json:"id" jsonschema:"initiative ID, for example I-1"`
	Promote promoteChoice `json:"promote,omitempty"`
}

func initiativeArchiveSchema() (*jsonschema.Schema, error) {
	return jsonschema.For[initiativeArchiveIn](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{reflect.TypeFor[promoteChoice](): promoteSchema()},
	})
}

func (t *tools) initiativeArchive(ctx context.Context, _ *mcp.CallToolRequest, in initiativeArchiveIn) (*mcp.CallToolResult, service.InitiativeView, error) {
	id, err := domain.ParseID(domain.KindInitiative, in.ID)
	if err != nil {
		return nil, service.InitiativeView{}, err
	}
	var choice service.DocumentChoice
	if choice, err = service.ParseDocumentChoice(in.Promote.items); err != nil {
		return nil, service.InitiativeView{}, err
	}
	v, err := t.svc.ArchiveInitiative(ctx, id, choice)
	var need *service.DocumentChoiceError
	if errors.As(err, &need) {
		return nil, v, promoteHint(need)
	}
	return nil, v, toolError(err)
}

func promoteHint(e *service.DocumentChoiceError) error {
	suggested := `promote: []`
	if ids := e.SuggestedIDs(); ids != "" {
		suggested = `promote: ["` + strings.ReplaceAll(ids, ",", `", "`) + `"]`
	}
	return fmt.Errorf("%v\nRerun initiative_archive with the user's choice: %s (the suggestion), promote: \"all\", or promote: \"none\". Ask the user if unsure.", e, suggested)
}

func (t *tools) initiativeUnarchive(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, service.InitiativeView, error) {
	id, err := domain.ParseID(domain.KindInitiative, in.ID)
	if err != nil {
		return nil, service.InitiativeView{}, err
	}
	v, err := t.svc.UnarchiveInitiative(ctx, id)
	return nil, v, toolError(err)
}

func (t *tools) docArchive(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, service.DocumentView, error) {
	id, err := domain.ParseID(domain.KindDocument, in.ID)
	if err != nil {
		return nil, service.DocumentView{}, err
	}
	v, err := t.svc.ArchiveDocument(ctx, id)
	return nil, v, toolError(err)
}

func (t *tools) docUnarchive(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, service.DocumentView, error) {
	id, err := domain.ParseID(domain.KindDocument, in.ID)
	if err != nil {
		return nil, service.DocumentView{}, err
	}
	v, err := t.svc.UnarchiveDocument(ctx, id)
	return nil, v, toolError(err)
}

type docMoveIn struct {
	ID   string `json:"id" jsonschema:"document ID, for example D-4"`
	Slug string `json:"slug,omitempty" jsonschema:"optional new slug applied with the move; use it to resolve a slug clash"`
}

func (t *tools) docPromote(ctx context.Context, _ *mcp.CallToolRequest, in docMoveIn) (*mcp.CallToolResult, service.DocumentView, error) {
	id, err := domain.ParseID(domain.KindDocument, in.ID)
	if err != nil {
		return nil, service.DocumentView{}, err
	}
	v, err := t.svc.PromoteDocument(ctx, id, in.Slug)
	return nil, v, toolError(err)
}

func (t *tools) docReturn(ctx context.Context, _ *mcp.CallToolRequest, in docMoveIn) (*mcp.CallToolResult, service.DocumentView, error) {
	id, err := domain.ParseID(domain.KindDocument, in.ID)
	if err != nil {
		return nil, service.DocumentView{}, err
	}
	v, err := t.svc.ReturnDocument(ctx, id, in.Slug)
	return nil, v, toolError(err)
}

type idIn struct {
	ID string `json:"id" jsonschema:"entity ID, for example I-1, M-2, T-3, or D-4"`
}

func (t *tools) initiativeGet(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, service.InitiativeView, error) {
	id, err := domain.ParseID(domain.KindInitiative, in.ID)
	if err != nil {
		return nil, service.InitiativeView{}, err
	}
	v, err := t.svc.GetInitiative(ctx, id)
	return nil, v, toolError(err)
}

type initiativeUpdateIn struct {
	ID         string  `json:"id" jsonschema:"initiative ID, for example I-1"`
	Title      *string `json:"title,omitempty" jsonschema:"new title"`
	Brief      *string `json:"brief,omitempty" jsonschema:"new brief, replacing the old one"`
	ParentCard *string `json:"parent_card,omitempty" jsonschema:"Jira key of the card to attach this initiative's card to, for example PROJ-123; an empty string removes the parent; omit the field to leave it unchanged"`
}

func (t *tools) initiativeUpdate(ctx context.Context, _ *mcp.CallToolRequest, in initiativeUpdateIn) (*mcp.CallToolResult, service.InitiativeView, error) {
	id, err := domain.ParseID(domain.KindInitiative, in.ID)
	if err != nil {
		return nil, service.InitiativeView{}, err
	}
	v, err := t.svc.UpdateInitiative(ctx, id, service.InitiativePatch{Title: in.Title, Brief: in.Brief, ParentCard: in.ParentCard})
	return nil, v, toolError(err)
}

type deleteIn struct {
	ID    string `json:"id" jsonschema:"entity ID, for example M-2"`
	Force bool   `json:"force,omitempty" jsonschema:"delete despite the cascade or task progress; only with the user's explicit approval"`
}

type deleteDocIn struct {
	ID string `json:"id" jsonschema:"document ID, for example D-4"`
}

type deleteNoteIn struct {
	ID string `json:"id" jsonschema:"note ID, for example N-3"`
}

func (t *tools) initiativeDelete(ctx context.Context, _ *mcp.CallToolRequest, in deleteIn) (*mcp.CallToolResult, service.DeleteView, error) {
	id, err := domain.ParseID(domain.KindInitiative, in.ID)
	if err != nil {
		return nil, service.DeleteView{}, err
	}
	v, err := t.svc.DeleteInitiative(ctx, id, in.Force)
	return nil, v, toolError(err)
}

// ---- milestones ----

func parseIDs(k domain.Kind, raw []string) ([]int64, error) {
	ids := []int64{}
	for _, r := range raw {
		id, err := domain.ParseID(k, r)
		if err != nil {
			return nil, fmt.Errorf("depends_on: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

type milestoneCreateIn struct {
	InitiativeID string   `json:"initiative_id" jsonschema:"initiative ID, for example I-1"`
	Title        string   `json:"title" jsonschema:"milestone title"`
	Description  string   `json:"description,omitempty" jsonschema:"what the milestone delivers and how it will be reviewed"`
	Position     int      `json:"position,omitempty" jsonschema:"order within the initiative; defaults to last"`
	Slug         string   `json:"slug,omitempty" jsonschema:"optional slug, used in the branch name; derived from title when omitted"`
	DependsOn    []string `json:"depends_on,omitempty" jsonschema:"IDs of milestones this one depends on, for example M-15; may be in other initiatives; cycles are refused"`
}

func (t *tools) milestoneCreate(ctx context.Context, _ *mcp.CallToolRequest, in milestoneCreateIn) (*mcp.CallToolResult, service.MilestoneView, error) {
	iID, err := domain.ParseID(domain.KindInitiative, in.InitiativeID)
	if err != nil {
		return nil, service.MilestoneView{}, err
	}
	deps, err := parseIDs(domain.KindMilestone, in.DependsOn)
	if err != nil {
		return nil, service.MilestoneView{}, err
	}
	v, err := t.svc.CreateMilestone(ctx, service.MilestoneInput{
		InitiativeID: iID, Title: in.Title, Description: in.Description, Position: in.Position, Slug: in.Slug, DependsOn: deps,
	})
	return nil, v, toolError(err)
}

type milestoneListIn struct {
	InitiativeID    string `json:"initiative_id,omitempty" jsonschema:"only this initiative"`
	IncludeArchived bool   `json:"include_archived,omitempty" jsonschema:"also include archived initiatives and documents, and their contents"`
}

type milestoneList struct {
	Milestones []service.MilestoneView `json:"milestones"`
}

func (t *tools) milestoneList(ctx context.Context, _ *mcp.CallToolRequest, in milestoneListIn) (*mcp.CallToolResult, milestoneList, error) {
	iID, err := parseOptionalID(domain.KindInitiative, in.InitiativeID)
	if err != nil {
		return nil, milestoneList{}, err
	}
	ms, err := t.svc.ListMilestones(ctx, iID, service.ListOptions{IncludeArchived: in.IncludeArchived})
	return nil, milestoneList{Milestones: ms}, toolError(err)
}

type milestoneDetail struct {
	service.MilestoneView
	WaitingOn []loop.DependencyFinding `json:"waiting_on,omitempty" jsonschema:"unsatisfied dependencies and why; omitted when none or when git cannot be checked"`
	Tasks     []service.TaskView       `json:"tasks"`
}

func (t *tools) milestoneGet(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, milestoneDetail, error) {
	id, err := domain.ParseID(domain.KindMilestone, in.ID)
	if err != nil {
		return nil, milestoneDetail{}, err
	}
	m, err := t.svc.GetMilestone(ctx, id)
	if err != nil {
		return nil, milestoneDetail{}, toolError(err)
	}
	tasks, err := t.svc.ListTasks(ctx, service.TaskFilter{MilestoneID: id, IncludeArchived: true})
	return nil, milestoneDetail{MilestoneView: m, WaitingOn: t.waitingOn(ctx, m), Tasks: tasks}, toolError(err)
}

// waitingOn lists m's unsatisfied dependencies. It is nil when drift cannot be
// checked or any check fails.
func (t *tools) waitingOn(ctx context.Context, m service.MilestoneView) []loop.DependencyFinding {
	if len(m.DependsOn) == 0 || m.Status == service.MilestoneDone || m.Status == service.MilestoneCancelled {
		return nil
	}
	if _, err := t.driftInput(ctx, m.ID); err != nil {
		return nil
	}
	var fs []loop.DependencyFinding
	for _, ref := range m.DependsOn {
		depID, err := domain.ParseID(domain.KindMilestone, ref)
		if err != nil {
			return nil
		}
		dm, err := t.svc.GetMilestone(ctx, depID)
		if err != nil {
			return nil
		}
		f, err := loop.ClassifyDependency(ctx, dm, func(ctx context.Context) (drift.Input, error) {
			return t.driftInput(ctx, ref)
		})
		if err != nil {
			return nil
		}
		fs = append(fs, f)
	}
	return loop.Unsatisfied(fs)
}

type milestoneUpdateIn struct {
	ID          string    `json:"id" jsonschema:"milestone ID, for example M-1"`
	Title       *string   `json:"title,omitempty" jsonschema:"new title"`
	Description *string   `json:"description,omitempty" jsonschema:"new description"`
	Position    *int      `json:"position,omitempty" jsonschema:"new position, at least 1"`
	DependsOn   *[]string `json:"depends_on,omitempty" jsonschema:"IDs of milestones this one depends on, for example M-15; omit to leave unchanged, [] to clear, otherwise replaces the whole set; may be in other initiatives; cycles are refused"`
}

func (t *tools) milestoneUpdate(ctx context.Context, _ *mcp.CallToolRequest, in milestoneUpdateIn) (*mcp.CallToolResult, service.MilestoneView, error) {
	id, err := domain.ParseID(domain.KindMilestone, in.ID)
	if err != nil {
		return nil, service.MilestoneView{}, err
	}
	var deps *[]int64
	if in.DependsOn != nil {
		ids, err := parseIDs(domain.KindMilestone, *in.DependsOn)
		if err != nil {
			return nil, service.MilestoneView{}, err
		}
		deps = &ids
	}
	v, err := t.svc.UpdateMilestone(ctx, id, service.MilestonePatch{Title: in.Title, Description: in.Description, Position: in.Position, DependsOn: deps})
	return nil, v, toolError(err)
}

func (t *tools) milestoneDelete(ctx context.Context, _ *mcp.CallToolRequest, in deleteIn) (*mcp.CallToolResult, service.DeleteView, error) {
	id, err := domain.ParseID(domain.KindMilestone, in.ID)
	if err != nil {
		return nil, service.DeleteView{}, err
	}
	v, err := t.svc.DeleteMilestone(ctx, id, in.Force)
	return nil, v, toolError(err)
}

type milestoneDriftIn struct {
	MilestoneID string `json:"milestone_id" jsonschema:"milestone ID, for example M-1"`
}

var errDriftUnavailable = errors.New("drift checking is unavailable in this server; run ralph milestone reconcile from the project instead")

// driftInput parses the milestone ID and builds its drift check input.
func (t *tools) driftInput(ctx context.Context, milestoneID string) (drift.Input, error) {
	if t.newDriftInput == nil {
		return drift.Input{}, errDriftUnavailable
	}
	id, err := domain.ParseID(domain.KindMilestone, milestoneID)
	if err != nil {
		return drift.Input{}, err
	}
	in, err := t.newDriftInput(ctx, id)
	return in, toolError(err)
}

func (t *tools) milestoneDrift(ctx context.Context, _ *mcp.CallToolRequest, in milestoneDriftIn) (*mcp.CallToolResult, drift.Report, error) {
	din, err := t.driftInput(ctx, in.MilestoneID)
	if err != nil {
		return nil, drift.Report{}, err
	}
	r, err := drift.Check(ctx, din)
	return nil, r, toolError(err)
}

type milestoneReconcileIn struct {
	MilestoneID string   `json:"milestone_id" jsonschema:"milestone ID, for example M-1"`
	Branch      *string  `json:"branch,omitempty" jsonschema:"point the record at this existing local branch, e.g. after a rename"`
	Reopen      []string `json:"reopen,omitempty" jsonschema:"DONE tasks of the milestone to send back to TODO, for example T-3"`
	Reason      string   `json:"reason,omitempty" jsonschema:"why the tasks are reopened; required with reopen and recorded on each task"`
	Accept      bool     `json:"accept,omitempty" jsonschema:"record the branch tip, base and each DONE task's matched commit"`
}

type milestoneReconcileOut struct {
	Before drift.Report `json:"before" jsonschema:"the drift report before any action"`
	After  drift.Report `json:"after" jsonschema:"the drift report after the actions; equal to before when no action was given"`
}

func (t *tools) milestoneReconcile(ctx context.Context, _ *mcp.CallToolRequest, in milestoneReconcileIn) (*mcp.CallToolResult, milestoneReconcileOut, error) {
	din, err := t.driftInput(ctx, in.MilestoneID)
	if err != nil {
		return nil, milestoneReconcileOut{}, err
	}
	var a drift.Actions
	if in.Branch != nil {
		if a.Branch = strings.TrimSpace(*in.Branch); a.Branch == "" {
			return nil, milestoneReconcileOut{}, fmt.Errorf("%w: branch must not be empty; omit it to keep the recorded branch", domain.ErrInvalid)
		}
	}
	for _, s := range in.Reopen {
		id, err := domain.ParseID(domain.KindTask, s)
		if err != nil {
			return nil, milestoneReconcileOut{}, fmt.Errorf("reopen: %w", err)
		}
		if !slices.Contains(a.Reopen, id) {
			a.Reopen = append(a.Reopen, id)
		}
	}
	a.Reason, a.Accept = in.Reason, in.Accept
	before, after, err := drift.Apply(ctx, din, a)
	if err != nil {
		if errors.Is(err, domain.ErrInvalid) {
			return nil, milestoneReconcileOut{}, fmt.Errorf("%v (nothing was changed; review the report from milestone_drift, then adjust branch, reopen, reason, or accept)", err)
		}
		return nil, milestoneReconcileOut{}, toolError(err)
	}
	return nil, milestoneReconcileOut{Before: before, After: after}, nil
}

// ---- tasks ----

type taskCreateIn struct {
	MilestoneID        string `json:"milestone_id" jsonschema:"milestone ID, for example M-1"`
	Title              string `json:"title" jsonschema:"imperative task title"`
	Description        string `json:"description,omitempty" jsonschema:"what to change and how, with enough context for an agent that has not seen this conversation"`
	AcceptanceCriteria string `json:"acceptance_criteria,omitempty" jsonschema:"verifiable conditions, ideally tests, that show the task is done"`
	Position           int    `json:"position,omitempty" jsonschema:"order within the milestone; defaults to last"`
}

func (t *tools) taskCreate(ctx context.Context, _ *mcp.CallToolRequest, in taskCreateIn) (*mcp.CallToolResult, service.TaskView, error) {
	mID, err := domain.ParseID(domain.KindMilestone, in.MilestoneID)
	if err != nil {
		return nil, service.TaskView{}, err
	}
	v, err := t.svc.CreateTask(ctx, service.TaskInput{
		MilestoneID: mID, Title: in.Title, Description: in.Description,
		AcceptanceCriteria: in.AcceptanceCriteria, Position: in.Position,
	})
	return nil, v, toolError(err)
}

type taskImportIn struct {
	MilestoneID        string `json:"milestone_id" jsonschema:"milestone ID, for example M-1"`
	Title              string `json:"title" jsonschema:"imperative task title"`
	Description        string `json:"description,omitempty" jsonschema:"what the task is; required for TODO"`
	AcceptanceCriteria string `json:"acceptance_criteria,omitempty" jsonschema:"verifiable conditions; required for TODO"`
	Status             string `json:"status" jsonschema:"PROPOSED, TODO, BLOCKED, DONE, or CANCELLED; IN_PROGRESS is not allowed"`
	Reason             string `json:"reason" jsonschema:"why this status was chosen; becomes the blocked reason for BLOCKED"`
	Source             string `json:"source,omitempty" jsonschema:"where the item came from, for example a tracker file or key"`
	Position           int    `json:"position,omitempty" jsonschema:"order within the milestone; defaults to last"`
}

func (t *tools) taskImport(ctx context.Context, _ *mcp.CallToolRequest, in taskImportIn) (*mcp.CallToolResult, service.TaskView, error) {
	mID, err := domain.ParseID(domain.KindMilestone, in.MilestoneID)
	if err != nil {
		return nil, service.TaskView{}, err
	}
	st, err := domain.ParseStatus(in.Status)
	if err != nil {
		return nil, service.TaskView{}, err
	}
	v, err := t.svc.ImportTask(ctx, service.ImportTaskInput{
		MilestoneID: mID, Title: in.Title, Description: in.Description,
		AcceptanceCriteria: in.AcceptanceCriteria, Status: st,
		Reason: in.Reason, Source: in.Source, Position: in.Position,
	})
	return nil, v, toolError(err)
}

type taskListIn struct {
	MilestoneID     string   `json:"milestone_id,omitempty" jsonschema:"only this milestone"`
	InitiativeID    string   `json:"initiative_id,omitempty" jsonschema:"only this initiative"`
	Statuses        []string `json:"statuses,omitempty" jsonschema:"only these statuses: PROPOSED, TODO, IN_PROGRESS, BLOCKED, CANCELLED, DONE"`
	IncludeArchived bool     `json:"include_archived,omitempty" jsonschema:"also include archived initiatives and documents, and their contents"`
}

type taskList struct {
	Tasks []service.TaskView `json:"tasks"`
}

func (t *tools) taskList(ctx context.Context, _ *mcp.CallToolRequest, in taskListIn) (*mcp.CallToolResult, taskList, error) {
	mID, err := parseOptionalID(domain.KindMilestone, in.MilestoneID)
	if err != nil {
		return nil, taskList{}, err
	}
	iID, err := parseOptionalID(domain.KindInitiative, in.InitiativeID)
	if err != nil {
		return nil, taskList{}, err
	}
	var statuses []domain.Status
	for _, s := range in.Statuses {
		st, err := domain.ParseStatus(s)
		if err != nil {
			return nil, taskList{}, err
		}
		statuses = append(statuses, st)
	}
	tasks, err := t.svc.ListTasks(ctx, service.TaskFilter{MilestoneID: mID, InitiativeID: iID, Statuses: statuses, IncludeArchived: in.IncludeArchived})
	return nil, taskList{Tasks: tasks}, toolError(err)
}

func (t *tools) taskGet(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, service.TaskView, error) {
	id, err := domain.ParseID(domain.KindTask, in.ID)
	if err != nil {
		return nil, service.TaskView{}, err
	}
	v, err := t.svc.GetTask(ctx, id)
	return nil, v, toolError(err)
}

type taskUpdateIn struct {
	ID                 string  `json:"id" jsonschema:"task ID, for example T-1"`
	Title              *string `json:"title,omitempty" jsonschema:"new title"`
	Description        *string `json:"description,omitempty" jsonschema:"new description"`
	AcceptanceCriteria *string `json:"acceptance_criteria,omitempty" jsonschema:"new acceptance criteria"`
	Position           *int    `json:"position,omitempty" jsonschema:"new position, at least 1"`
}

func (t *tools) taskUpdate(ctx context.Context, _ *mcp.CallToolRequest, in taskUpdateIn) (*mcp.CallToolResult, service.TaskView, error) {
	id, err := domain.ParseID(domain.KindTask, in.ID)
	if err != nil {
		return nil, service.TaskView{}, err
	}
	v, err := t.svc.UpdateTask(ctx, id, service.TaskPatch{
		Title: in.Title, Description: in.Description, AcceptanceCriteria: in.AcceptanceCriteria, Position: in.Position,
	})
	return nil, v, toolError(err)
}

type taskSetStatusIn struct {
	ID     string `json:"id" jsonschema:"task ID, for example T-1"`
	Status string `json:"status" jsonschema:"target status: PROPOSED, TODO, IN_PROGRESS, BLOCKED, CANCELLED, or DONE"`
	Reason string `json:"reason,omitempty" jsonschema:"why; required for BLOCKED and recorded as a note"`
}

func (t *tools) taskSetStatus(ctx context.Context, _ *mcp.CallToolRequest, in taskSetStatusIn) (*mcp.CallToolResult, service.TaskView, error) {
	id, err := domain.ParseID(domain.KindTask, in.ID)
	if err != nil {
		return nil, service.TaskView{}, err
	}
	to, err := domain.ParseStatus(in.Status)
	if err != nil {
		return nil, service.TaskView{}, err
	}
	v, err := t.svc.SetTaskStatus(ctx, id, to, in.Reason)
	return nil, v, toolError(err)
}

type taskAddNoteIn struct {
	ID   string `json:"id" jsonschema:"task ID, for example T-1"`
	Body string `json:"body" jsonschema:"the note"`
	Kind string `json:"kind,omitempty" jsonschema:"NOTE (default), ITERATION, or KICKBACK"`
}

func (t *tools) taskAddNote(ctx context.Context, _ *mcp.CallToolRequest, in taskAddNoteIn) (*mcp.CallToolResult, service.TaskView, error) {
	id, err := domain.ParseID(domain.KindTask, in.ID)
	if err != nil {
		return nil, service.TaskView{}, err
	}
	kind := domain.NoteGeneral
	if in.Kind != "" {
		if kind, err = domain.ParseNoteKind(in.Kind); err != nil {
			return nil, service.TaskView{}, err
		}
	}
	v, err := t.svc.AddTaskNote(ctx, id, kind, in.Body)
	return nil, v, toolError(err)
}

type taskNextIn struct {
	MilestoneID string `json:"milestone_id" jsonschema:"milestone ID, for example M-1"`
}

type taskNext struct {
	Task    *service.TaskView `json:"task,omitempty"`
	Message string            `json:"message,omitempty"`
}

func (t *tools) taskNext(ctx context.Context, _ *mcp.CallToolRequest, in taskNextIn) (*mcp.CallToolResult, taskNext, error) {
	mID, err := domain.ParseID(domain.KindMilestone, in.MilestoneID)
	if err != nil {
		return nil, taskNext{}, err
	}
	v, err := t.svc.NextTask(ctx, mID)
	if err != nil {
		return nil, taskNext{}, toolError(err)
	}
	if v == nil {
		return nil, taskNext{Message: "no IN_PROGRESS or TODO tasks remain in " + in.MilestoneID}, nil
	}
	return nil, taskNext{Task: v}, nil
}

func (t *tools) taskDelete(ctx context.Context, _ *mcp.CallToolRequest, in deleteIn) (*mcp.CallToolResult, service.DeleteView, error) {
	id, err := domain.ParseID(domain.KindTask, in.ID)
	if err != nil {
		return nil, service.DeleteView{}, err
	}
	v, err := t.svc.DeleteTask(ctx, id, in.Force)
	return nil, v, toolError(err)
}

func (t *tools) noteDelete(ctx context.Context, _ *mcp.CallToolRequest, in deleteNoteIn) (*mcp.CallToolResult, service.DeleteView, error) {
	id, err := domain.ParseID(domain.KindNote, in.ID)
	if err != nil {
		return nil, service.DeleteView{}, err
	}
	v, err := t.svc.DeleteNote(ctx, id)
	return nil, v, toolError(err)
}

// ---- documents ----

type docCreateIn struct {
	Kind         string `json:"kind" jsonschema:"one of brief, architecture, design, decision, reference, note"`
	Title        string `json:"title" jsonschema:"document title"`
	Body         string `json:"body,omitempty" jsonschema:"markdown body"`
	InitiativeID string `json:"initiative_id,omitempty" jsonschema:"scope to this initiative; omit for project-wide"`
	Slug         string `json:"slug,omitempty" jsonschema:"optional slug; derived from title when omitted"`
}

func (t *tools) docCreate(ctx context.Context, _ *mcp.CallToolRequest, in docCreateIn) (*mcp.CallToolResult, service.DocumentView, error) {
	kind, err := domain.ParseDocKind(in.Kind)
	if err != nil {
		return nil, service.DocumentView{}, err
	}
	iID, err := parseOptionalID(domain.KindInitiative, in.InitiativeID)
	if err != nil {
		return nil, service.DocumentView{}, err
	}
	v, err := t.svc.CreateDocument(ctx, service.DocumentInput{InitiativeID: iID, Kind: kind, Title: in.Title, Body: in.Body, Slug: in.Slug})
	return nil, v, toolError(err)
}

type docListIn struct {
	InitiativeID    string `json:"initiative_id,omitempty" jsonschema:"only this initiative's documents"`
	ProjectOnly     bool   `json:"project_only,omitempty" jsonschema:"only project-wide documents"`
	Kind            string `json:"kind,omitempty" jsonschema:"only this kind"`
	IncludeArchived bool   `json:"include_archived,omitempty" jsonschema:"also include archived initiatives and documents, and their contents"`
}

type docList struct {
	Documents []service.DocumentView `json:"documents"`
}

func (t *tools) docList(ctx context.Context, _ *mcp.CallToolRequest, in docListIn) (*mcp.CallToolResult, docList, error) {
	iID, err := parseOptionalID(domain.KindInitiative, in.InitiativeID)
	if err != nil {
		return nil, docList{}, err
	}
	f := service.DocumentFilter{InitiativeID: iID, ProjectOnly: in.ProjectOnly, IncludeArchived: in.IncludeArchived}
	if in.Kind != "" {
		if f.Kind, err = domain.ParseDocKind(in.Kind); err != nil {
			return nil, docList{}, err
		}
	}
	docs, err := t.svc.ListDocuments(ctx, f)
	return nil, docList{Documents: docs}, toolError(err)
}

func (t *tools) docGet(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, service.DocumentView, error) {
	id, err := domain.ParseID(domain.KindDocument, in.ID)
	if err != nil {
		return nil, service.DocumentView{}, err
	}
	v, err := t.svc.GetDocument(ctx, id)
	return nil, v, toolError(err)
}

type docUpdateIn struct {
	ID    string  `json:"id" jsonschema:"document ID, for example D-1"`
	Kind  *string `json:"kind,omitempty" jsonschema:"new kind"`
	Title *string `json:"title,omitempty" jsonschema:"new title"`
	Body  *string `json:"body,omitempty" jsonschema:"new body, replacing the old one entirely"`
}

func (t *tools) docUpdate(ctx context.Context, _ *mcp.CallToolRequest, in docUpdateIn) (*mcp.CallToolResult, service.DocumentView, error) {
	id, err := domain.ParseID(domain.KindDocument, in.ID)
	if err != nil {
		return nil, service.DocumentView{}, err
	}
	p := service.DocumentPatch{Title: in.Title, Body: in.Body}
	if in.Kind != nil {
		k := domain.DocKind(*in.Kind)
		p.Kind = &k
	}
	v, err := t.svc.UpdateDocument(ctx, id, p)
	return nil, v, toolError(err)
}

func (t *tools) docDelete(ctx context.Context, _ *mcp.CallToolRequest, in deleteDocIn) (*mcp.CallToolResult, service.DeleteView, error) {
	id, err := domain.ParseID(domain.KindDocument, in.ID)
	if err != nil {
		return nil, service.DeleteView{}, err
	}
	v, err := t.svc.DeleteDocument(ctx, id)
	return nil, v, toolError(err)
}

// ---- search & status ----

type searchIn struct {
	Query           string   `json:"query" jsonschema:"natural-language query"`
	Kinds           []string `json:"kinds,omitempty" jsonschema:"only these kinds: initiative, milestone, task, document"`
	InitiativeID    string   `json:"initiative_id,omitempty" jsonschema:"only this initiative"`
	Limit           int      `json:"limit,omitempty" jsonschema:"maximum results; defaults to the configured search.limit"`
	IncludeArchived bool     `json:"include_archived,omitempty" jsonschema:"also include archived initiatives and documents, and their contents"`
}

func (t *tools) search(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, service.SearchView, error) {
	var kinds []domain.Kind
	for _, k := range in.Kinds {
		kind, err := domain.ParseKind(k)
		if err != nil {
			return nil, service.SearchView{}, err
		}
		kinds = append(kinds, kind)
	}
	iID, err := parseOptionalID(domain.KindInitiative, in.InitiativeID)
	if err != nil {
		return nil, service.SearchView{}, err
	}
	v, err := t.svc.Search(ctx, service.SearchInput{Query: in.Query, Kinds: kinds, InitiativeID: iID, Limit: in.Limit, IncludeArchived: in.IncludeArchived})
	return nil, v, toolError(err)
}

type statusIn struct {
	InitiativeID    string `json:"initiative_id,omitempty" jsonschema:"only this initiative"`
	IncludeArchived bool   `json:"include_archived,omitempty" jsonschema:"also include archived initiatives and documents, and their contents"`
}

func (t *tools) status(ctx context.Context, _ *mcp.CallToolRequest, in statusIn) (*mcp.CallToolResult, service.StatusView, error) {
	iID, err := parseOptionalID(domain.KindInitiative, in.InitiativeID)
	if err != nil {
		return nil, service.StatusView{}, err
	}
	v, err := t.svc.Status(ctx, iID, service.ListOptions{IncludeArchived: in.IncludeArchived})
	return nil, v, toolError(err)
}

type cardsSyncIn struct {
	InitiativeID    string `json:"initiative_id,omitempty" jsonschema:"only this initiative, for example I-8"`
	IncludeArchived bool   `json:"include_archived,omitempty" jsonschema:"also sync archived initiatives"`
}

func (t *tools) cardsSync(ctx context.Context, _ *mcp.CallToolRequest, in cardsSyncIn) (*mcp.CallToolResult, service.SyncReport, error) {
	iID, err := parseOptionalID(domain.KindInitiative, in.InitiativeID)
	if err != nil {
		return nil, service.SyncReport{}, err
	}
	v, err := t.svc.SyncCards(ctx, service.SyncOptions{InitiativeID: iID, IncludeArchived: in.IncludeArchived})
	if errors.Is(err, service.ErrNoTaskManager) && t.tmErr != nil {
		err = fmt.Errorf("task manager disabled: %w", t.tmErr)
	}
	return nil, v, toolError(err)
}
