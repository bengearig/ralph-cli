package service

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/search"
	"github.com/bengearig/ralph/internal/store"
)

func initiativeItem(i domain.Initiative) search.Item {
	return search.Item{
		Owner:        store.Owner{Kind: domain.KindInitiative, ID: i.ID},
		InitiativeID: i.ID,
		Title:        i.Title,
		Body:         i.Brief,
	}
}

func milestoneItem(m domain.Milestone) search.Item {
	return search.Item{
		Owner:        store.Owner{Kind: domain.KindMilestone, ID: m.ID},
		InitiativeID: m.InitiativeID,
		Title:        m.Title,
		Body:         m.Description,
	}
}

func taskItem(t domain.Task, initiativeID int64) search.Item {
	body := t.Description
	if t.AcceptanceCriteria != "" {
		body += "\n\n## Acceptance criteria\n\n" + t.AcceptanceCriteria
	}
	return search.Item{
		Owner:        store.Owner{Kind: domain.KindTask, ID: t.ID},
		InitiativeID: initiativeID,
		Title:        t.Title,
		Body:         body,
	}
}

func documentItem(d domain.Document) search.Item {
	var initiative int64
	if d.InitiativeID != nil {
		initiative = *d.InitiativeID
	}
	return search.Item{
		Owner:        store.Owner{Kind: domain.KindDocument, ID: d.ID},
		InitiativeID: initiative,
		Title:        d.Title,
		Body:         d.Body,
	}
}

// index updates the search index after a successful write. The write is
// already committed, so failures surface as a warning rather than an error.
func (s *Service) indexItem(ctx context.Context, it search.Item) string {
	if s.index == nil {
		return ""
	}
	res, err := s.index.Index(ctx, it)
	if err != nil {
		s.log.Warn("indexing failed", "owner", domain.FormatID(it.Owner.Kind, it.Owner.ID), "err", err)
		return fmt.Sprintf("saved, but indexing failed (run `ralph index --rebuild`): %v", err)
	}
	return res.Warning
}

func (s *Service) indexInitiative(ctx context.Context, i domain.Initiative) string {
	return s.indexItem(ctx, initiativeItem(i))
}

func (s *Service) indexMilestone(ctx context.Context, m domain.Milestone) string {
	return s.indexItem(ctx, milestoneItem(m))
}

func (s *Service) indexTask(ctx context.Context, t domain.Task, initiativeID int64) string {
	return s.indexItem(ctx, taskItem(t, initiativeID))
}

func (s *Service) indexDocument(ctx context.Context, d domain.Document) string {
	return s.indexItem(ctx, documentItem(d))
}

// IndexReport summarizes a reindex.
type IndexReport struct {
	Rechunked int    `json:"rechunked"`
	Embedded  int    `json:"embedded"`
	Remaining int    `json:"remaining"`
	Mode      string `json:"mode"`
}

// Reindex embeds stale chunks. With rebuild it first rechunks every entity,
// which picks up chunking-config changes and repairs a damaged index.
func (s *Service) Reindex(ctx context.Context, rebuild bool, progress func(done, total int)) (IndexReport, error) {
	if s.index == nil {
		return IndexReport{}, fmt.Errorf("search index is not configured")
	}
	var rep IndexReport
	if rebuild {
		items, err := s.allItems(ctx)
		if err != nil {
			return rep, err
		}
		// Rechunk without embedding; Backfill embeds in batches afterwards.
		chunkOnly := *s.index
		chunkOnly.Embedder = nil
		for _, it := range items {
			if _, err := chunkOnly.Index(ctx, it); err != nil {
				return rep, err
			}
			rep.Rechunked++
		}
	}
	if s.index.Embedder == nil {
		rep.Mode = search.ModeKeyword
		return rep, nil
	}
	rep.Mode = search.ModeVector
	bf, err := s.index.Backfill(ctx, 32, progress)
	rep.Embedded, rep.Remaining = bf.Embedded, bf.Remaining
	return rep, err
}

func (s *Service) allItems(ctx context.Context) ([]search.Item, error) {
	var items []search.Item
	inits, err := s.store.ListInitiatives(ctx)
	if err != nil {
		return nil, err
	}
	for _, i := range inits {
		items = append(items, initiativeItem(i))
	}
	ms, err := s.store.ListMilestones(ctx, 0)
	if err != nil {
		return nil, err
	}
	initiativeOf := map[int64]int64{}
	for _, m := range ms {
		initiativeOf[m.ID] = m.InitiativeID
		items = append(items, milestoneItem(m))
	}
	tasks, err := s.store.ListTasks(ctx, store.TaskFilter{})
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		items = append(items, taskItem(t, initiativeOf[t.MilestoneID]))
	}
	docs, err := s.store.ListDocuments(ctx, store.DocumentFilter{})
	if err != nil {
		return nil, err
	}
	for _, d := range docs {
		items = append(items, documentItem(d))
	}
	return items, nil
}

// SearchInput describes a search request.
type SearchInput struct {
	Query        string
	Kinds        []domain.Kind
	InitiativeID int64
	Limit        int
}

// Search finds initiatives, milestones, tasks, and documents relevant to a
// query, annotating each hit with its title.
func (s *Service) Search(ctx context.Context, in SearchInput) (SearchView, error) {
	if err := domain.RequireText("query", in.Query); err != nil {
		return SearchView{}, err
	}
	if s.index == nil {
		return SearchView{}, fmt.Errorf("search index is not configured")
	}
	res, err := s.index.Search(ctx, search.Query{
		Text: in.Query, Kinds: in.Kinds, InitiativeID: in.InitiativeID, Limit: in.Limit,
	})
	if err != nil {
		return SearchView{}, err
	}
	out := SearchView{Mode: res.Mode, Warnings: res.Warnings, Hits: make([]HitView, 0, len(res.Hits))}
	for _, h := range res.Hits {
		title, err := s.title(ctx, h.Owner)
		if err != nil {
			s.log.Warn("search hit references a missing entity", "owner", h.Owner, "err", err)
			continue
		}
		hv := HitView{
			ID:      domain.FormatID(h.Owner.Kind, h.Owner.ID),
			Kind:    string(h.Owner.Kind),
			Title:   title,
			Score:   h.Score,
			Snippet: h.Snippet,
		}
		if h.InitiativeID != 0 {
			hv.InitiativeID = domain.FormatID(domain.KindInitiative, h.InitiativeID)
		}
		out.Hits = append(out.Hits, hv)
	}
	return out, nil
}

func (s *Service) title(ctx context.Context, o store.Owner) (string, error) {
	switch o.Kind {
	case domain.KindInitiative:
		i, err := s.store.GetInitiative(ctx, o.ID)
		return i.Title, err
	case domain.KindMilestone:
		m, err := s.store.GetMilestone(ctx, o.ID)
		return m.Title, err
	case domain.KindTask:
		t, err := s.store.GetTask(ctx, o.ID)
		return t.Title, err
	case domain.KindDocument:
		d, err := s.store.GetDocument(ctx, o.ID)
		return d.Title, err
	}
	return "", fmt.Errorf("unknown kind %q", o.Kind)
}

// Status summarizes progress for one initiative, or all when id is zero.
func (s *Service) Status(ctx context.Context, initiativeID int64) (StatusView, error) {
	var inits []domain.Initiative
	if initiativeID != 0 {
		i, err := s.store.GetInitiative(ctx, initiativeID)
		if err != nil {
			return StatusView{}, err
		}
		inits = []domain.Initiative{i}
	} else {
		var err error
		if inits, err = s.store.ListInitiatives(ctx); err != nil {
			return StatusView{}, err
		}
	}
	out := StatusView{Initiatives: make([]InitiativeStatus, 0, len(inits))}
	for _, i := range inits {
		ms, err := s.ListMilestones(ctx, i.ID)
		if err != nil {
			return StatusView{}, err
		}
		docs, err := s.store.ListDocuments(ctx, store.DocumentFilter{InitiativeID: i.ID})
		if err != nil {
			return StatusView{}, err
		}
		counts := map[string]int{}
		for _, m := range ms {
			for k, n := range m.TaskCounts {
				counts[k] += n
			}
		}
		out.Initiatives = append(out.Initiatives, InitiativeStatus{
			ID:         domain.FormatID(domain.KindInitiative, i.ID),
			Title:      i.Title,
			TaskCounts: counts,
			Milestones: ms,
			Documents:  len(docs),
		})
	}
	return out, nil
}

// FormatCounts renders status counts in lifecycle order, e.g. "TODO 2, DONE 1".
func FormatCounts(counts map[string]int) string {
	var parts []string
	seen := map[string]bool{}
	for _, st := range domain.Statuses() {
		if n := counts[string(st)]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", st, n))
		}
		seen[string(st)] = true
	}
	var extra []string
	for k, n := range counts {
		if !seen[k] && n > 0 {
			extra = append(extra, fmt.Sprintf("%s %d", k, n))
		}
	}
	sort.Strings(extra)
	parts = append(parts, extra...)
	if len(parts) == 0 {
		return "no tasks"
	}
	return strings.Join(parts, ", ")
}
