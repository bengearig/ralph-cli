package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/store"
	"github.com/bengearig/ralph/internal/taskmanager"
)

// ErrReconcileNeedsScope means applying was asked for without a creation date scope.
var ErrReconcileNeedsScope = fmt.Errorf("%w: applying requires a creation date scope", domain.ErrInvalid)

// ErrNoCanceledStatus means applying was asked for with no canceled status configured.
var ErrNoCanceledStatus = fmt.Errorf("%w: applying requires a canceled status; set task_manager.jira.canceled_status", domain.ErrInvalid)

// ReconcileOptions scopes ReconcileCards. A zero CreatedAfter means no date
// scope. With Apply set, orphans are cancelled and Progress, when set, is
// called with each card's final item.
type ReconcileOptions struct {
	CreatedAfter time.Time
	Apply        bool
	Progress     func(ReconcileItem)
}

// ReconcileItem is the outcome for one candidate card. ID is the ralph ID
// parsed from the summary, empty when none parsed. Store is the store
// stamp from the summary, empty when unstamped. Reason explains a skip.
type ReconcileItem struct {
	Key     string `json:"key"`
	URL     string `json:"url,omitempty"`
	ID      string `json:"id,omitempty"`
	Store   string `json:"store,omitempty"`
	Summary string `json:"summary"`
	Status  string `json:"status,omitempty"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
}

// ReconcileReport counts and lists the candidates, in the provider's order.
type ReconcileReport struct {
	Orphans   int             `json:"orphans"`
	Skipped   int             `json:"skipped"`
	Cancelled int             `json:"cancelled"`
	Failed    int             `json:"failed"`
	Items     []ReconcileItem `json:"items"`
}

const (
	reconcileOrphan    = "orphan"
	reconcileSkipped   = "skipped"
	reconcileCancelled = "cancelled"
	reconcileFailed    = "failed"
)

var summaryID = regexp.MustCompile(`^(?:\[([a-z0-9]{2,6})\] )?([IMT])-([0-9]+): `)

// parseSummary returns the store stamp and the normalized ralph ID leading a
// card summary. Each is empty when absent; a malformed stamp yields neither.
func parseSummary(summary string) (stamp, id string) {
	m := summaryID.FindStringSubmatch(summary)
	if m == nil {
		return "", ""
	}
	n, err := strconv.ParseInt(m[3], 10, 64)
	if err != nil {
		return "", ""
	}
	return m[1], m[2] + "-" + strconv.FormatInt(n, 10)
}

func (r *ReconcileReport) add(it ReconcileItem) {
	switch it.Outcome {
	case reconcileOrphan:
		r.Orphans++
	case reconcileSkipped:
		r.Skipped++
	}
	r.Items = append(r.Items, it)
}

// ReconcileCards finds the provider's open cards and classifies each as an
// orphan or skipped. With Apply it then cancels the orphans in the provider.
// It writes nothing to the store.
func (s *Service) ReconcileCards(ctx context.Context, opts ReconcileOptions) (ReconcileReport, error) {
	report := ReconcileReport{Items: []ReconcileItem{}}
	if s.taskManager == nil {
		return report, ErrNoTaskManager
	}
	if s.inTx {
		return report, errors.New("cards cannot be reconciled inside a transaction")
	}
	if opts.Apply {
		if opts.CreatedAfter.IsZero() {
			return report, ErrReconcileNeedsScope
		}
		if s.cardCanceledStatus == "" {
			return report, ErrNoCanceledStatus
		}
	}
	finder, ok := s.taskManager.(taskmanager.CardFinder)
	if !ok {
		return report, fmt.Errorf("%w: %s cannot search for cards", domain.ErrInvalid, s.taskManager.Name())
	}
	cands, err := finder.FindOpenCards(ctx, taskmanager.CardSearch{CreatedAfter: opts.CreatedAfter})
	if err != nil {
		return report, err
	}

	recorded, exists, err := s.reconcileLiveSets(ctx)
	if err != nil {
		return report, err
	}
	for _, c := range cands {
		stamp, id := parseSummary(c.Summary)
		it := ReconcileItem{Key: c.Key, URL: c.URL, ID: id, Store: stamp, Summary: c.Summary, Status: c.Status}
		switch {
		case recorded[c.Key] != "":
			it.Outcome, it.Reason = reconcileSkipped, "key is recorded for "+recorded[c.Key]
		case it.ID == "":
			it.Outcome, it.Reason = reconcileSkipped, "summary has no ralph ID"
		case exists[it.ID]:
			it.Outcome, it.Reason = reconcileSkipped, it.ID+" exists in ralph with a different or no recorded card"
		default:
			it.Outcome = reconcileOrphan
		}
		report.add(it)
	}
	if !opts.Apply {
		return report, nil
	}
	for n := range report.Items {
		it := &report.Items[n]
		if it.Outcome != reconcileOrphan {
			continue
		}
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if err := s.taskManager.SetStatus(ctx, it.Key, string(domain.StatusCancelled)); err != nil {
			it.Outcome, it.Reason = reconcileFailed, err.Error()
			report.Failed++
		} else {
			it.Outcome = reconcileCancelled
			report.Cancelled++
		}
		if opts.Progress != nil {
			opts.Progress(*it)
		}
	}
	return report, nil
}

// reconcileLiveSets maps each recorded card key to its display ID, and lists
// the display IDs of every resource, archived and cancelled ones included.
func (s *Service) reconcileLiveSets(ctx context.Context) (map[string]string, map[string]bool, error) {
	cards, err := s.store.ListCards(ctx, s.taskManager.Name())
	if err != nil {
		return nil, nil, err
	}
	recorded := map[string]string{}
	for _, c := range cards {
		if c.Key != "" {
			recorded[c.Key] = domain.FormatID(c.Kind, c.EntityID)
		}
	}
	exists := map[string]bool{}
	inits, err := s.store.ListInitiatives(ctx, false)
	if err != nil {
		return nil, nil, err
	}
	for _, i := range inits {
		exists[domain.FormatID(domain.KindInitiative, i.ID)] = true
	}
	ms, err := s.store.ListMilestones(ctx, 0, false)
	if err != nil {
		return nil, nil, err
	}
	for _, m := range ms {
		exists[domain.FormatID(domain.KindMilestone, m.ID)] = true
	}
	tasks, err := s.store.ListTasks(ctx, store.TaskFilter{})
	if err != nil {
		return nil, nil, err
	}
	for _, t := range tasks {
		exists[domain.FormatID(domain.KindTask, t.ID)] = true
	}
	return recorded, exists, nil
}
