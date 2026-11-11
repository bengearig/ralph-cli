package service

import (
	"time"

	"github.com/bengearig/ralph/internal/domain"
)

// Views are the wire shapes shared by CLI JSON output and MCP tool results.
// IDs use display form (T-12) so agents and humans quote the same strings.

type InitiativeView struct {
	ID           string    `json:"id"`
	Slug         string    `json:"slug"`
	Title        string    `json:"title"`
	Brief        string    `json:"brief"`
	CreatedAt    string    `json:"created_at"`
	UpdatedAt    string    `json:"updated_at"`
	ArchivedAt   string    `json:"archived_at,omitempty"`
	IndexWarning string    `json:"index_warning,omitempty"`
	CardWarning  string    `json:"card_warning,omitempty"`
	Card         *CardView `json:"card,omitempty"`
}

type MilestoneView struct {
	ID           string         `json:"id"`
	InitiativeID string         `json:"initiative_id"`
	Slug         string         `json:"slug"`
	Title        string         `json:"title"`
	Description  string         `json:"description"`
	Position     int            `json:"position"`
	Status       string         `json:"status"`
	TaskCounts   map[string]int `json:"task_counts"`
	DependsOn    []string       `json:"depends_on,omitempty"`
	Branch       string         `json:"branch,omitempty"`
	BaseCommit   string         `json:"base_commit,omitempty"`
	HeadCommit   string         `json:"head_commit,omitempty"`
	CreatedAt    string         `json:"created_at"`
	UpdatedAt    string         `json:"updated_at"`
	Archived     bool           `json:"archived,omitempty"`
	IndexWarning string         `json:"index_warning,omitempty"`
	CardWarning  string         `json:"card_warning,omitempty"`
	Card         *CardView      `json:"card,omitempty"`
}

type TaskView struct {
	ID                 string     `json:"id"`
	MilestoneID        string     `json:"milestone_id"`
	Title              string     `json:"title"`
	Description        string     `json:"description"`
	AcceptanceCriteria string     `json:"acceptance_criteria"`
	Status             string     `json:"status"`
	AllowedNext        []string   `json:"allowed_next"`
	Position           int        `json:"position"`
	FailureCount       int        `json:"failure_count"`
	BlockedReason      string     `json:"blocked_reason,omitempty"`
	Commit             string     `json:"commit,omitempty"`
	Notes              []NoteView `json:"notes,omitempty"`
	CreatedAt          string     `json:"created_at"`
	UpdatedAt          string     `json:"updated_at"`
	Archived           bool       `json:"archived,omitempty"`
	IndexWarning       string     `json:"index_warning,omitempty"`
	CardWarning        string     `json:"card_warning,omitempty"`
	Card               *CardView  `json:"card,omitempty"`
}

type NoteView struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

type DocumentView struct {
	ID                 string `json:"id"`
	InitiativeID       string `json:"initiative_id,omitempty"`
	OriginInitiativeID string `json:"origin_initiative_id,omitempty"`
	Kind               string `json:"kind"`
	Slug               string `json:"slug"`
	Title              string `json:"title"`
	Body               string `json:"body,omitempty"`
	CreatedAt          string `json:"created_at"`
	UpdatedAt          string `json:"updated_at"`
	ArchivedAt         string `json:"archived_at,omitempty"`
	Archived           bool   `json:"archived,omitempty"`
	IndexWarning       string `json:"index_warning,omitempty"`
}

type SearchView struct {
	Mode     string    `json:"mode"`
	Hits     []HitView `json:"hits"`
	Warnings []string  `json:"warnings,omitempty"`
}

type HitView struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"`
	Title        string  `json:"title"`
	InitiativeID string  `json:"initiative_id,omitempty"`
	Score        float64 `json:"score"`
	Snippet      string  `json:"snippet"`
}

type StatusView struct {
	Initiatives []InitiativeStatus `json:"initiatives"`
}

type InitiativeStatus struct {
	ID         string          `json:"id"`
	Title      string          `json:"title"`
	TaskCounts map[string]int  `json:"task_counts"`
	Milestones []MilestoneView `json:"milestones"`
	Documents  int             `json:"documents"`
	Card       *CardView       `json:"card,omitempty"`
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func tsIfSet(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return ts(t)
}

func initiativeView(i domain.Initiative) InitiativeView {
	return InitiativeView{
		ID:         domain.FormatID(domain.KindInitiative, i.ID),
		Slug:       i.Slug,
		Title:      i.Title,
		Brief:      i.Brief,
		CreatedAt:  ts(i.CreatedAt),
		UpdatedAt:  ts(i.UpdatedAt),
		ArchivedAt: tsIfSet(i.ArchivedAt),
	}
}

func milestoneView(m domain.Milestone, tasks []domain.Task, archived bool) MilestoneView {
	counts := map[string]int{}
	for _, t := range tasks {
		counts[string(t.Status)]++
	}
	return MilestoneView{
		ID:           domain.FormatID(domain.KindMilestone, m.ID),
		InitiativeID: domain.FormatID(domain.KindInitiative, m.InitiativeID),
		Slug:         m.Slug,
		Title:        m.Title,
		Description:  m.Description,
		Position:     m.Position,
		Status:       MilestoneStatus(tasks),
		TaskCounts:   counts,
		Branch:       m.Git.Branch,
		BaseCommit:   m.Git.BaseCommit,
		HeadCommit:   m.Git.HeadCommit,
		CreatedAt:    ts(m.CreatedAt),
		UpdatedAt:    ts(m.UpdatedAt),
		Archived:     archived,
	}
}

func taskView(t domain.Task, notes []domain.TaskNote, archived bool) TaskView {
	next := make([]string, 0, 4)
	for _, s := range t.Status.AllowedNext() {
		next = append(next, string(s))
	}
	v := TaskView{
		ID:                 domain.FormatID(domain.KindTask, t.ID),
		MilestoneID:        domain.FormatID(domain.KindMilestone, t.MilestoneID),
		Title:              t.Title,
		Description:        t.Description,
		AcceptanceCriteria: t.AcceptanceCriteria,
		Status:             string(t.Status),
		AllowedNext:        next,
		Position:           t.Position,
		FailureCount:       t.FailureCount,
		BlockedReason:      t.BlockedReason,
		Commit:             t.Commit,
		CreatedAt:          ts(t.CreatedAt),
		UpdatedAt:          ts(t.UpdatedAt),
		Archived:           archived,
	}
	for _, n := range notes {
		v.Notes = append(v.Notes, NoteView{ID: domain.FormatID(domain.KindNote, n.ID), Kind: string(n.Kind), Body: n.Body, CreatedAt: ts(n.CreatedAt)})
	}
	return v
}

// initiativeArchived is whether the document's initiative is archived.
func documentView(d domain.Document, withBody, initiativeArchived bool) DocumentView {
	v := DocumentView{
		ID:         domain.FormatID(domain.KindDocument, d.ID),
		Kind:       string(d.Kind),
		Slug:       d.Slug,
		Title:      d.Title,
		CreatedAt:  ts(d.CreatedAt),
		UpdatedAt:  ts(d.UpdatedAt),
		ArchivedAt: tsIfSet(d.ArchivedAt),
		Archived:   !d.ArchivedAt.IsZero() || initiativeArchived,
	}
	if d.InitiativeID != nil {
		v.InitiativeID = domain.FormatID(domain.KindInitiative, *d.InitiativeID)
	}
	if d.OriginInitiativeID != nil {
		v.OriginInitiativeID = domain.FormatID(domain.KindInitiative, *d.OriginInitiativeID)
	}
	if withBody {
		v.Body = d.Body
	}
	return v
}

// Derived milestone statuses.
const (
	MilestoneEmpty        = domain.MilestoneEmpty
	MilestoneArticulating = domain.MilestoneArticulating
	MilestoneReady        = domain.MilestoneReady
	MilestoneInProgress   = domain.MilestoneInProgress
	MilestoneBlocked      = domain.MilestoneBlocked
	MilestoneDone         = domain.MilestoneDone
	MilestoneCancelled    = domain.MilestoneCancelled
)

// MilestoneStatus derives a milestone's status from its tasks.
func MilestoneStatus(tasks []domain.Task) string {
	if len(tasks) == 0 {
		return MilestoneEmpty
	}
	c := map[domain.Status]int{}
	for _, t := range tasks {
		c[t.Status]++
	}
	switch {
	case c[domain.StatusCancelled] == len(tasks):
		return MilestoneCancelled
	case c[domain.StatusDone]+c[domain.StatusCancelled] == len(tasks):
		return MilestoneDone
	case c[domain.StatusBlocked] > 0:
		return MilestoneBlocked
	case c[domain.StatusInProgress] > 0 || c[domain.StatusDone] > 0:
		return MilestoneInProgress
	case c[domain.StatusProposed] > 0:
		return MilestoneArticulating
	}
	return MilestoneReady
}

// DeleteView reports a delete: the target and the rows removed per kind
// (initiatives, milestones, tasks, notes, documents); zero counts are omitted.
type DeleteView struct {
	ID      string         `json:"id"`
	Kind    string         `json:"kind"`
	Deleted map[string]int `json:"deleted"`
	// Dependents lists milestones outside the deleted set that depend on one inside it ("M-5 depends on M-2").
	Dependents []string `json:"dependents,omitempty"`
	// Cards lists the cards moved to the canceled status, or in a preview those that will be.
	Cards []DeletedCard `json:"cards,omitempty"`
}

// DeletedCard is a card of a deleted resource and whether it was moved to the canceled status.
type DeletedCard struct {
	ID    string `json:"id"`
	Key   string `json:"key"`
	URL   string `json:"url"`
	Moved bool   `json:"moved"`
	Error string `json:"error,omitempty"`
}

type ImportLinkView struct {
	Document string `json:"document"`
	Relation string `json:"relation"`
}

type ImportView struct {
	Path        string           `json:"path"`
	Status      string           `json:"status"`
	ContentHash string           `json:"content_hash"`
	Size        int64            `json:"size"`
	Note        string           `json:"note,omitempty"`
	QueuedAt    string           `json:"queued_at"`
	ProcessedAt string           `json:"processed_at,omitempty"`
	Changed     bool             `json:"changed"`
	Links       []ImportLinkView `json:"links"`
	Content     string           `json:"content,omitempty"`
}

func importView(im domain.Import, links []ImportLinkView, withContent bool) ImportView {
	v := ImportView{
		Path:        im.Path,
		Status:      string(im.Status),
		ContentHash: im.ContentHash,
		Size:        im.Size,
		Note:        im.Note,
		QueuedAt:    ts(im.QueuedAt),
		Changed:     im.LastDoneHash != "" && im.LastDoneHash != im.ContentHash,
		Links:       links,
	}
	if !im.ProcessedAt.IsZero() {
		v.ProcessedAt = ts(im.ProcessedAt)
	}
	if withContent {
		v.Content = im.Content
	}
	return v
}
