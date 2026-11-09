package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/drift"
	"github.com/bengearig/ralph/internal/service"
)

type empty struct{}

// ---- initiatives ----

type initiativeCreateIn struct {
	Title string `json:"title" jsonschema:"short title for the initiative"`
	Brief string `json:"brief,omitempty" jsonschema:"the instructions exactly as received"`
	Slug  string `json:"slug,omitempty" jsonschema:"optional slug; derived from title when omitted"`
}

// initiativeCreateOut adds MCP-only guidance to the shared view (ADR 10).
type initiativeCreateOut struct {
	service.InitiativeView
	ConfirmOutline bool `json:"confirm_outline" jsonschema:"true: ask the user to approve the outline before creating milestones and tasks; false: show the outline and create it right away"`
}

func (t *tools) initiativeCreate(ctx context.Context, _ *mcp.CallToolRequest, in initiativeCreateIn) (*mcp.CallToolResult, initiativeCreateOut, error) {
	v, err := t.svc.CreateInitiative(ctx, service.InitiativeInput{Title: in.Title, Brief: in.Brief, Slug: in.Slug})
	if err != nil {
		return nil, initiativeCreateOut{}, toolError(err)
	}
	return nil, initiativeCreateOut{InitiativeView: v, ConfirmOutline: t.confirmOutline}, nil
}

type initiativeList struct {
	Initiatives []service.InitiativeView `json:"initiatives"`
}

func (t *tools) initiativeList(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, initiativeList, error) {
	all, err := t.svc.ListInitiatives(ctx)
	return nil, initiativeList{Initiatives: all}, toolError(err)
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
	ID    string  `json:"id" jsonschema:"initiative ID, for example I-1"`
	Title *string `json:"title,omitempty" jsonschema:"new title"`
	Brief *string `json:"brief,omitempty" jsonschema:"new brief, replacing the old one"`
}

func (t *tools) initiativeUpdate(ctx context.Context, _ *mcp.CallToolRequest, in initiativeUpdateIn) (*mcp.CallToolResult, service.InitiativeView, error) {
	id, err := domain.ParseID(domain.KindInitiative, in.ID)
	if err != nil {
		return nil, service.InitiativeView{}, err
	}
	v, err := t.svc.UpdateInitiative(ctx, id, service.InitiativePatch{Title: in.Title, Brief: in.Brief})
	return nil, v, toolError(err)
}

// ---- milestones ----

type milestoneCreateIn struct {
	InitiativeID string `json:"initiative_id" jsonschema:"initiative ID, for example I-1"`
	Title        string `json:"title" jsonschema:"milestone title"`
	Description  string `json:"description,omitempty" jsonschema:"what the milestone delivers and how it will be reviewed"`
	Position     int    `json:"position,omitempty" jsonschema:"order within the initiative; defaults to last"`
	Slug         string `json:"slug,omitempty" jsonschema:"optional slug, used in the branch name; derived from title when omitted"`
}

func (t *tools) milestoneCreate(ctx context.Context, _ *mcp.CallToolRequest, in milestoneCreateIn) (*mcp.CallToolResult, service.MilestoneView, error) {
	iID, err := domain.ParseID(domain.KindInitiative, in.InitiativeID)
	if err != nil {
		return nil, service.MilestoneView{}, err
	}
	v, err := t.svc.CreateMilestone(ctx, service.MilestoneInput{
		InitiativeID: iID, Title: in.Title, Description: in.Description, Position: in.Position, Slug: in.Slug,
	})
	return nil, v, toolError(err)
}

type milestoneListIn struct {
	InitiativeID string `json:"initiative_id,omitempty" jsonschema:"only this initiative"`
}

type milestoneList struct {
	Milestones []service.MilestoneView `json:"milestones"`
}

func (t *tools) milestoneList(ctx context.Context, _ *mcp.CallToolRequest, in milestoneListIn) (*mcp.CallToolResult, milestoneList, error) {
	iID, err := parseOptionalID(domain.KindInitiative, in.InitiativeID)
	if err != nil {
		return nil, milestoneList{}, err
	}
	ms, err := t.svc.ListMilestones(ctx, iID)
	return nil, milestoneList{Milestones: ms}, toolError(err)
}

type milestoneDetail struct {
	service.MilestoneView
	Tasks []service.TaskView `json:"tasks"`
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
	tasks, err := t.svc.ListTasks(ctx, service.TaskFilter{MilestoneID: id})
	return nil, milestoneDetail{MilestoneView: m, Tasks: tasks}, toolError(err)
}

type milestoneUpdateIn struct {
	ID          string  `json:"id" jsonschema:"milestone ID, for example M-1"`
	Title       *string `json:"title,omitempty" jsonschema:"new title"`
	Description *string `json:"description,omitempty" jsonschema:"new description"`
	Position    *int    `json:"position,omitempty" jsonschema:"new position, at least 1"`
}

func (t *tools) milestoneUpdate(ctx context.Context, _ *mcp.CallToolRequest, in milestoneUpdateIn) (*mcp.CallToolResult, service.MilestoneView, error) {
	id, err := domain.ParseID(domain.KindMilestone, in.ID)
	if err != nil {
		return nil, service.MilestoneView{}, err
	}
	v, err := t.svc.UpdateMilestone(ctx, id, service.MilestonePatch{Title: in.Title, Description: in.Description, Position: in.Position})
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

type taskListIn struct {
	MilestoneID  string   `json:"milestone_id,omitempty" jsonschema:"only this milestone"`
	InitiativeID string   `json:"initiative_id,omitempty" jsonschema:"only this initiative"`
	Statuses     []string `json:"statuses,omitempty" jsonschema:"only these statuses: PROPOSED, TODO, IN_PROGRESS, BLOCKED, CANCELLED, DONE"`
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
	tasks, err := t.svc.ListTasks(ctx, service.TaskFilter{MilestoneID: mID, InitiativeID: iID, Statuses: statuses})
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
	InitiativeID string `json:"initiative_id,omitempty" jsonschema:"only this initiative's documents"`
	ProjectOnly  bool   `json:"project_only,omitempty" jsonschema:"only project-wide documents"`
	Kind         string `json:"kind,omitempty" jsonschema:"only this kind"`
}

type docList struct {
	Documents []service.DocumentView `json:"documents"`
}

func (t *tools) docList(ctx context.Context, _ *mcp.CallToolRequest, in docListIn) (*mcp.CallToolResult, docList, error) {
	iID, err := parseOptionalID(domain.KindInitiative, in.InitiativeID)
	if err != nil {
		return nil, docList{}, err
	}
	f := service.DocumentFilter{InitiativeID: iID, ProjectOnly: in.ProjectOnly}
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

// ---- search & status ----

type searchIn struct {
	Query        string   `json:"query" jsonschema:"natural-language query"`
	Kinds        []string `json:"kinds,omitempty" jsonschema:"only these kinds: initiative, milestone, task, document"`
	InitiativeID string   `json:"initiative_id,omitempty" jsonschema:"only this initiative"`
	Limit        int      `json:"limit,omitempty" jsonschema:"maximum results; defaults to the configured search.limit"`
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
	v, err := t.svc.Search(ctx, service.SearchInput{Query: in.Query, Kinds: kinds, InitiativeID: iID, Limit: in.Limit})
	return nil, v, toolError(err)
}

type statusIn struct {
	InitiativeID string `json:"initiative_id,omitempty" jsonschema:"only this initiative"`
}

func (t *tools) status(ctx context.Context, _ *mcp.CallToolRequest, in statusIn) (*mcp.CallToolResult, service.StatusView, error) {
	iID, err := parseOptionalID(domain.KindInitiative, in.InitiativeID)
	if err != nil {
		return nil, service.StatusView{}, err
	}
	v, err := t.svc.Status(ctx, iID)
	return nil, v, toolError(err)
}
