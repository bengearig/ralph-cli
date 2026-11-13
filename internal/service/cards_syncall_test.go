package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/store"
	"github.com/bengearig/ralph/internal/taskmanager"
	tmfake "github.com/bengearig/ralph/internal/taskmanager/fake"
)

type syncTree struct {
	init  int64
	ms    []int64
	tasks [][]int64
}

// buildSyncTree creates an initiative, two milestones, and three tasks with no provider, so no rows exist.
func buildSyncTree(t *testing.T) (env, syncTree, *tmfake.Provider) {
	t.Helper()
	ctx := context.Background()
	e := newEnv(t)
	i, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Init", Brief: "brief"})
	if err != nil {
		t.Fatal(err)
	}
	tr := syncTree{init: iid(t, i.ID)}
	for _, count := range []int{2, 1} {
		m, err := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: tr.init, Title: "Mile"})
		if err != nil {
			t.Fatal(err)
		}
		mID := mid(t, m.ID)
		tr.ms = append(tr.ms, mID)
		var ids []int64
		for k := 0; k < count; k++ {
			tk, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mID, Title: "Task", Description: "d"})
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, tid(t, tk.ID))
		}
		tr.tasks = append(tr.tasks, ids)
	}
	e, p := withProvider(e)
	return e, tr, p
}

func (tr syncTree) order() []string {
	out := []string{domain.FormatID(domain.KindInitiative, tr.init)}
	for n, m := range tr.ms {
		out = append(out, domain.FormatID(domain.KindMilestone, m))
		for _, t := range tr.tasks[n] {
			out = append(out, domain.FormatID(domain.KindTask, t))
		}
	}
	return out
}

func checkCounts(t *testing.T, r SyncReport) {
	t.Helper()
	got := map[string]int{}
	for _, it := range r.Items {
		got[it.Outcome]++
	}
	if got["created"] != r.Created || got["updated"] != r.Updated || got["in_sync"] != r.InSync ||
		got["skipped"] != r.Skipped || got["failed"] != r.Failed {
		t.Errorf("counts %+v do not match items %v", r, got)
	}
}

func TestSyncCardsCreatesMissingParentsFirst(t *testing.T) {
	ctx := context.Background()
	e, tr, p := buildSyncTree(t)
	r, err := e.svc.SyncCards(ctx, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	order := tr.order()
	if r.Created != len(order) || len(r.Items) != len(order) || r.Failed != 0 {
		t.Fatalf("report %+v", r)
	}
	checkCounts(t, r)
	cs := creates(p.Calls())
	keys := map[string]string{}
	for n, id := range order {
		if r.Items[n].ID != id || r.Items[n].Outcome != "created" || r.Items[n].Key == "" {
			t.Errorf("item %d = %+v, want %s created", n, r.Items[n], id)
		}
		if cs[n].Request.ID != id {
			t.Errorf("create %d = %s, want %s", n, cs[n].Request.ID, id)
		}
		keys[id] = r.Items[n].Key
	}
	if cs[0].Request.ParentKey != "" {
		t.Errorf("initiative parent = %q", cs[0].Request.ParentKey)
	}
	for n, m := range tr.ms {
		mName := domain.FormatID(domain.KindMilestone, m)
		for _, c := range cs {
			if c.Request.ID == mName && c.Request.ParentKey != keys[order[0]] {
				t.Errorf("milestone %d parent = %q", n, c.Request.ParentKey)
			}
			for _, tk := range tr.tasks[n] {
				if c.Request.ID == domain.FormatID(domain.KindTask, tk) && c.Request.ParentKey != keys[mName] {
					t.Errorf("task %d parent = %q, want %q", tk, c.Request.ParentKey, keys[mName])
				}
			}
		}
	}
	for _, it := range r.Items {
		kind, id := rowOf(t, it.ID)
		if c, _ := e.svc.Card(ctx, kind, id, "fake"); c == nil || c.Key != it.Key {
			t.Errorf("row for %s = %+v", it.ID, c)
		}
	}

	n := len(p.Calls())
	r, err = e.svc.SyncCards(ctx, SyncOptions{})
	if err != nil || r.InSync != len(order) || r.Created+r.Updated+r.Skipped+r.Failed != 0 || len(p.Calls()) != n {
		t.Fatalf("second sync: %v %+v, %d new calls", err, r, len(p.Calls())-n)
	}
	checkCounts(t, r)
}

func TestSyncCardsUpdatesEditedTask(t *testing.T) {
	ctx := context.Background()
	e, tr, p := buildSyncTree(t)
	if _, err := e.svc.SyncCards(ctx, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	e.svc.taskManager = nil
	title := "Edited"
	if _, err := e.svc.UpdateTask(ctx, tr.tasks[0][0], TaskPatch{Title: &title}); err != nil {
		t.Fatal(err)
	}
	e.svc.taskManager = p
	n := len(p.Calls())
	r, err := e.svc.SyncCards(ctx, SyncOptions{})
	if err != nil || r.Updated != 1 || r.InSync != len(tr.order())-1 {
		t.Fatalf("%v %+v", err, r)
	}
	var updates int
	for _, c := range p.Calls()[n:] {
		if c.Method == "Update" {
			updates++
		}
	}
	if updates != 1 {
		t.Errorf("Update calls = %d", updates)
	}
}

func TestSyncCardsRetryUsesTimeoutWindow(t *testing.T) {
	ctx := context.Background()
	e, tr, p := buildSyncTree(t)
	e.svc.cardTimeout = 50 * time.Millisecond
	e.svc.cardClaimStale = time.Hour
	p.SetError(errors.New("down"))
	e.svc.createCard(ctx, domain.KindInitiative, tr.init, e.svc.cardClaimStale)
	p.SetError(nil)

	r, err := e.svc.SyncCards(ctx, SyncOptions{InitiativeID: tr.init})
	if err != nil {
		t.Fatal(err)
	}
	if r.Items[0].Outcome != "skipped" || r.Items[0].Error == "" {
		t.Fatalf("recent attempt: %+v", r.Items[0])
	}
	if r.Failed != len(tr.order())-1 || len(p.Calls()) != 0 {
		t.Errorf("children of an in-flight parent: %+v, %d calls", r, len(p.Calls()))
	}
	time.Sleep(100 * time.Millisecond)
	r, err = e.svc.SyncCards(ctx, SyncOptions{InitiativeID: tr.init})
	if err != nil || r.Created != len(tr.order()) {
		t.Fatalf("after the timeout: %v %+v", err, r)
	}
}

func TestSyncCardsSetsStatusAfterCreate(t *testing.T) {
	ctx := context.Background()
	e, tr, p := buildSyncTree(t)
	id := tr.tasks[0][0]
	for _, st := range []domain.Status{domain.StatusTodo, domain.StatusInProgress} {
		if _, err := e.svc.store.GetTask(ctx, id); err != nil {
			t.Fatal(err)
		}
		tk, _ := e.svc.store.GetTask(ctx, id)
		tk.Status = st
		if err := e.svc.store.UpdateTask(ctx, &tk); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.svc.SyncCards(ctx, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	calls := p.Calls()
	name := domain.FormatID(domain.KindTask, id)
	for n, c := range calls {
		if c.Method == "Create" && c.Request.ID == name {
			if n+1 >= len(calls) || calls[n+1].Method != "SetStatus" || calls[n+1].Key == "" || calls[n+1].Request.Status != "IN_PROGRESS" {
				t.Fatalf("after create: %+v", calls[n+1:])
			}
			return
		}
	}
	t.Fatal("task never created")
}

func TestSyncCardsCancelled(t *testing.T) {
	ctx := context.Background()
	e, tr, p := buildSyncTree(t)
	cancelled := tr.tasks[1][0]
	if _, err := e.svc.SetTaskStatus(ctx, cancelled, domain.StatusCancelled, ""); err != nil {
		t.Fatal(err)
	}
	r, err := e.svc.SyncCards(ctx, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	name := domain.FormatID(domain.KindTask, cancelled)
	mName := domain.FormatID(domain.KindMilestone, tr.ms[1])
	for _, it := range r.Items {
		if (it.ID == name || it.ID == mName) && (it.Outcome != "skipped" || it.Error != "cancelled") {
			t.Errorf("%+v", it)
		}
	}
	for _, c := range creates(p.Calls()) {
		if c.Request.ID == name || c.Request.ID == mName {
			t.Errorf("cancelled work was created: %s", c.Request.ID)
		}
	}
	if r.Skipped != 2 {
		t.Errorf("skipped = %d", r.Skipped)
	}
	checkCounts(t, r)
}

func TestSyncCardsCancelledWithCardIsPushed(t *testing.T) {
	ctx := context.Background()
	e, tr, p := buildSyncTree(t)
	if _, err := e.svc.SyncCards(ctx, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	e.svc.taskManager = nil
	id := tr.tasks[0][0]
	if _, err := e.svc.SetTaskStatus(ctx, id, domain.StatusCancelled, ""); err != nil {
		t.Fatal(err)
	}
	e.svc.taskManager = p
	r, err := e.svc.SyncCards(ctx, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	name := domain.FormatID(domain.KindTask, id)
	for _, it := range r.Items {
		if it.ID == name && it.Outcome != "updated" {
			t.Errorf("%+v", it)
		}
	}
	if r.Skipped != 0 || r.Updated == 0 {
		t.Errorf("%+v", r)
	}
}

func TestSyncCardsParentFailure(t *testing.T) {
	ctx := context.Background()
	e, tr, p := buildSyncTree(t)
	boom := errors.New("boom")
	p.SetHook(func(_ int, method string, req taskmanager.CardRequest) error {
		if method == "Create" && req.Kind == domain.KindInitiative {
			return boom
		}
		return nil
	})
	r, err := e.svc.SyncCards(ctx, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	order := tr.order()
	if r.Failed != len(order) || len(p.Calls()) != 0 {
		t.Fatalf("%+v, %d calls", r, len(p.Calls()))
	}
	if !strings.Contains(r.Items[0].Error, "boom") {
		t.Errorf("initiative error = %q", r.Items[0].Error)
	}
	for _, it := range r.Items[1:] {
		if it.Outcome != "failed" || !strings.Contains(it.Error, "parent "+order[0]+" has no card") {
			t.Errorf("%+v", it)
		}
	}
	checkCounts(t, r)
}

func TestSyncCardsSingleTaskFailure(t *testing.T) {
	ctx := context.Background()
	e, tr, p := buildSyncTree(t)
	bad := domain.FormatID(domain.KindTask, tr.tasks[0][0])
	p.SetHook(func(_ int, method string, req taskmanager.CardRequest) error {
		if method == "Create" && req.ID == bad {
			return errors.New("nope")
		}
		return nil
	})
	r, err := e.svc.SyncCards(ctx, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := len(tr.order()) - 1
	if r.Failed != 1 || r.Created != want {
		t.Fatalf("%+v", r)
	}
	for _, it := range r.Items {
		if (it.ID == bad) != (it.Outcome == "failed") {
			t.Errorf("%+v", it)
		}
	}
	checkCounts(t, r)
}

func TestSyncCardsScope(t *testing.T) {
	ctx := context.Background()
	e, tr, p := buildSyncTree(t)
	e.svc.taskManager = nil
	other, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	e.svc.taskManager = p
	oid := iid(t, other.ID)

	r, err := e.svc.SyncCards(ctx, SyncOptions{InitiativeID: tr.init})
	if err != nil || len(r.Items) != len(tr.order()) {
		t.Fatalf("%v %+v", err, r)
	}
	if c, _ := e.svc.Card(ctx, domain.KindInitiative, oid, "fake"); c != nil {
		t.Errorf("other initiative got a card: %+v", c)
	}

	if _, err := e.svc.ArchiveInitiative(ctx, oid, DocumentChoice{}); err != nil {
		t.Fatal(err)
	}
	r, err = e.svc.SyncCards(ctx, SyncOptions{})
	if err != nil || r.Created != 0 {
		t.Fatalf("archived default: %v %+v", err, r)
	}
	r, err = e.svc.SyncCards(ctx, SyncOptions{IncludeArchived: true})
	if err != nil || r.Created != 1 || len(creates(p.Calls())) != len(tr.order())+1 {
		t.Fatalf("archived included: %v %+v", err, r)
	}

	if _, err := e.svc.SyncCards(ctx, SyncOptions{InitiativeID: 999}); err == nil {
		t.Error("unknown initiative: no error")
	}
}

func TestSyncCardsNotConfigured(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.SyncCards(context.Background(), SyncOptions{}); !errors.Is(err, ErrNoTaskManager) {
		t.Errorf("err = %v", err)
	}
}

func TestSyncCardsConcurrent(t *testing.T) {
	ctx := context.Background()
	e, tr, p := buildSyncTree(t)
	other := *e.svc
	svcs := []*Service{e.svc, &other}
	var wg sync.WaitGroup
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svcs[n].SyncCards(ctx, SyncOptions{}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	seen := map[string]int{}
	for _, c := range creates(p.Calls()) {
		seen[c.Request.ID]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("%s created %d times", id, n)
		}
	}
	if _, err := e.svc.SyncCards(ctx, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, id := range tr.order() {
		if seen[id] > 1 {
			t.Errorf("%s", id)
		}
	}
}

func rowOf(t *testing.T, id string) (domain.Kind, int64) {
	t.Helper()
	for _, k := range []domain.Kind{domain.KindInitiative, domain.KindMilestone, domain.KindTask} {
		if n, err := domain.ParseID(k, id); err == nil {
			return k, n
		}
	}
	t.Fatalf("bad id %s", id)
	return "", 0
}

func TestSyncCardsPreflightFailureCreatesNothing(t *testing.T) {
	ctx := context.Background()
	e, tr, p := buildSyncTree(t)
	e.svc.taskManager = p
	p.SetPreflightError(errors.New("initiative type is level 2"))
	report, err := e.svc.SyncCards(ctx, SyncOptions{})
	if err == nil || !strings.Contains(err.Error(), "nothing was synced") || !strings.Contains(err.Error(), "initiative type is level 2") {
		t.Fatalf("err = %v", err)
	}
	if len(report.Items) != 0 || len(p.Calls()) != 0 {
		t.Errorf("items %d, calls %d", len(report.Items), len(p.Calls()))
	}
	if _, err := e.svc.store.GetCard(ctx, domain.KindInitiative, tr.init, "fake"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("card row after failed preflight: %v", err)
	}

	p.SetPreflightError(nil)
	report, err = e.svc.SyncCards(ctx, SyncOptions{})
	if err != nil || report.Failed != 0 || report.Created == 0 || p.Preflights() != 2 {
		t.Errorf("err %v, report %+v, preflights %d", err, report, p.Preflights())
	}
}

type progressLog struct {
	calls []SyncProgress
}

func (l *progressLog) record(p SyncProgress) {
	if p.Item != nil {
		it := *p.Item
		p.Item = &it
	}
	l.calls = append(l.calls, p)
}

func (l *progressLog) check(t *testing.T, total int, r SyncReport) {
	t.Helper()
	if len(l.calls) != len(r.Items)+1 {
		t.Fatalf("%d callbacks for %d items", len(l.calls), len(r.Items))
	}
	if first := l.calls[0]; first.Done != 0 || first.Total != total || first.Item != nil {
		t.Errorf("first callback = %+v, want Done 0 Total %d nil Item", first, total)
	}
	for n, c := range l.calls[1:] {
		if c.Done != n+1 || c.Total != total || c.Item == nil || *c.Item != r.Items[n] {
			t.Errorf("callback %d = %+v, want item %+v", n+1, c, r.Items[n])
		}
	}
}

func TestSyncCardsProgressCountsEveryItem(t *testing.T) {
	ctx := context.Background()
	e, tr, p := buildSyncTree(t)
	e.svc.taskManager = nil
	for n := 0; n < 2; n++ {
		if _, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: tr.ms[1], Title: "More", Description: "d"}); err != nil {
			t.Fatal(err)
		}
	}
	e.svc.taskManager = p
	var l progressLog
	r, err := e.svc.SyncCards(ctx, SyncOptions{Progress: l.record})
	if err != nil {
		t.Fatal(err)
	}
	if r.Created != 8 || len(r.Items) != 8 {
		t.Fatalf("report %+v", r)
	}
	l.check(t, 8, r)
	checkCounts(t, r)

	l = progressLog{}
	r, err = e.svc.SyncCards(ctx, SyncOptions{Progress: l.record})
	if err != nil || r.InSync != 8 {
		t.Fatalf("%v %+v", err, r)
	}
	l.check(t, 8, r)
}

func TestSyncCardsProgressReportsSkippedAndFailed(t *testing.T) {
	ctx := context.Background()
	e, tr, p := buildSyncTree(t)
	skippedTask := tr.tasks[0][0]
	if _, err := e.svc.SetTaskStatus(ctx, skippedTask, domain.StatusCancelled, ""); err != nil {
		t.Fatal(err)
	}
	badTask := domain.FormatID(domain.KindTask, tr.tasks[0][1])
	badMilestone := domain.FormatID(domain.KindMilestone, tr.ms[1])
	p.SetHook(func(_ int, method string, req taskmanager.CardRequest) error {
		if method == "Create" && (req.ID == badTask || req.ID == badMilestone) {
			return errors.New("nope")
		}
		return nil
	})
	var l progressLog
	r, err := e.svc.SyncCards(ctx, SyncOptions{Progress: l.record})
	if err != nil {
		t.Fatal(err)
	}
	if r.Skipped != 1 || r.Failed != 3 || r.Created != 2 {
		t.Fatalf("report %+v", r)
	}
	l.check(t, len(tr.order()), r)
	outcomes := map[string]string{}
	for _, c := range l.calls[1:] {
		outcomes[c.Item.ID] = c.Item.Outcome
	}
	child := domain.FormatID(domain.KindTask, tr.tasks[1][0])
	if outcomes[domain.FormatID(domain.KindTask, skippedTask)] != "skipped" || outcomes[badTask] != "failed" || outcomes[child] != "failed" {
		t.Errorf("outcomes %v", outcomes)
	}
}

func TestSyncCardsProgressTotalFollowsScope(t *testing.T) {
	ctx := context.Background()
	e, tr, p := buildSyncTree(t)
	e.svc.taskManager = nil
	other, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	e.svc.taskManager = p

	total := func(opts SyncOptions) int {
		t.Helper()
		var l progressLog
		opts.Progress = l.record
		if _, err := e.svc.SyncCards(ctx, opts); err != nil {
			t.Fatal(err)
		}
		return l.calls[0].Total
	}
	want := len(tr.order())
	if got := total(SyncOptions{}); got != want+1 {
		t.Errorf("all initiatives total = %d, want %d", got, want+1)
	}
	if got := total(SyncOptions{InitiativeID: tr.init}); got != want {
		t.Errorf("one initiative total = %d, want %d", got, want)
	}
	if _, err := e.svc.ArchiveInitiative(ctx, iid(t, other.ID), DocumentChoice{}); err != nil {
		t.Fatal(err)
	}
	if got := total(SyncOptions{}); got != want {
		t.Errorf("archived excluded total = %d, want %d", got, want)
	}
	if got := total(SyncOptions{IncludeArchived: true}); got != want+1 {
		t.Errorf("archived included total = %d, want %d", got, want+1)
	}
}

func TestSyncCardsProgressNotCalledWhenNothingRuns(t *testing.T) {
	ctx := context.Background()
	called := func(SyncProgress) { t.Error("progress called") }
	if _, err := newEnv(t).svc.SyncCards(ctx, SyncOptions{Progress: called}); !errors.Is(err, ErrNoTaskManager) {
		t.Errorf("err = %v", err)
	}
	e, _, p := buildSyncTree(t)
	p.SetPreflightError(errors.New("bad"))
	if _, err := e.svc.SyncCards(ctx, SyncOptions{Progress: called}); err == nil {
		t.Error("preflight failure: no error")
	}
}

func TestSyncCardsProgressStopsWhenCancelled(t *testing.T) {
	e, tr, _ := buildSyncTree(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var l progressLog
	_, err := e.svc.SyncCards(ctx, SyncOptions{Progress: func(p SyncProgress) {
		l.record(p)
		if p.Done == 3 {
			cancel()
		}
	}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if last := l.calls[len(l.calls)-1]; last.Done != 3 || last.Done > last.Total || last.Total != len(tr.order()) {
		t.Errorf("last callback = %+v", last)
	}
}

func kindOffItems(r SyncReport) map[string]SyncItem {
	m := map[string]SyncItem{}
	for _, it := range r.Items {
		m[it.ID] = it
	}
	return m
}

func TestSyncCardsKindOffSkipsTasks(t *testing.T) {
	ctx := context.Background()
	e, tr, p := buildSyncTree(t)
	e.svc.cardKinds = kindsWithoutTask
	var l progressLog
	r, err := e.svc.SyncCards(ctx, SyncOptions{Progress: l.record})
	if err != nil {
		t.Fatal(err)
	}
	if r.Created != 3 || r.Skipped != 3 || r.Failed != 0 {
		t.Fatalf("%+v", r)
	}
	items := kindOffItems(r)
	for _, ts := range tr.tasks {
		for _, id := range ts {
			it := items[domain.FormatID(domain.KindTask, id)]
			if it.Outcome != "skipped" || it.Error != syncReasonKindOff {
				t.Errorf("%+v", it)
			}
		}
	}
	for _, c := range creates(p.Calls()) {
		if c.Request.Kind == domain.KindTask {
			t.Errorf("task created: %s", c.Request.ID)
		}
	}
	checkCounts(t, r)
	l.check(t, len(tr.order()), r)
}

func TestSyncCardsKindOffIgnoresFailedParent(t *testing.T) {
	ctx := context.Background()
	e, tr, p := buildSyncTree(t)
	e.svc.cardKinds = kindsWithoutTask
	p.SetHook(func(_ int, method string, req taskmanager.CardRequest) error {
		if method == "Create" && req.Kind == domain.KindMilestone {
			return errors.New("boom")
		}
		return nil
	})
	r, err := e.svc.SyncCards(ctx, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Created != 1 || r.Failed != 2 || r.Skipped != 3 {
		t.Fatalf("%+v", r)
	}
	items := kindOffItems(r)
	for _, ts := range tr.tasks {
		for _, id := range ts {
			if it := items[domain.FormatID(domain.KindTask, id)]; it.Outcome != "skipped" || it.Error != syncReasonKindOff {
				t.Errorf("%+v", it)
			}
		}
	}
	checkCounts(t, r)
}

func TestSyncCardsKindOffKeepsExistingCards(t *testing.T) {
	ctx := context.Background()
	e, tr, p := buildSyncTree(t)
	if _, err := e.svc.SyncCards(ctx, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	e.svc.taskManager = nil
	title := "Edited"
	if _, err := e.svc.UpdateTask(ctx, tr.tasks[0][0], TaskPatch{Title: &title}); err != nil {
		t.Fatal(err)
	}
	e.svc.taskManager = p
	e.svc.cardKinds = kindsWithoutTask
	r, err := e.svc.SyncCards(ctx, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Updated != 1 || r.InSync != len(tr.order())-1 || r.Skipped != 0 || r.Failed != 0 {
		t.Fatalf("%+v", r)
	}
	if it := kindOffItems(r)[domain.FormatID(domain.KindTask, tr.tasks[0][0])]; it.Outcome != "updated" {
		t.Errorf("%+v", it)
	}
}

func TestSyncCardsKindOffCancelledReportsKind(t *testing.T) {
	ctx := context.Background()
	e, tr, _ := buildSyncTree(t)
	e.svc.cardKinds = kindsWithoutTask
	id := tr.tasks[1][0]
	if _, err := e.svc.SetTaskStatus(ctx, id, domain.StatusCancelled, ""); err != nil {
		t.Fatal(err)
	}
	r, err := e.svc.SyncCards(ctx, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if it := kindOffItems(r)[domain.FormatID(domain.KindTask, id)]; it.Outcome != "skipped" || it.Error != syncReasonKindOff {
		t.Errorf("%+v", it)
	}
}

func TestSyncCardsKindOnKeepsCancelledReason(t *testing.T) {
	ctx := context.Background()
	e, tr, p := buildSyncTree(t)
	e.svc.cardKinds = []domain.Kind{domain.KindInitiative, domain.KindMilestone, domain.KindTask}
	id := tr.tasks[1][0]
	if _, err := e.svc.SetTaskStatus(ctx, id, domain.StatusCancelled, ""); err != nil {
		t.Fatal(err)
	}
	r, err := e.svc.SyncCards(ctx, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if it := kindOffItems(r)[domain.FormatID(domain.KindTask, id)]; it.Outcome != "skipped" || it.Error != syncReasonCancelled {
		t.Errorf("%+v", it)
	}
	if got := len(creates(p.Calls())); got != len(tr.order())-2 {
		t.Errorf("creates = %d", got)
	}
}

func TestSyncCardsEmptyKindsSkipsAllButUpdatesExisting(t *testing.T) {
	ctx := context.Background()
	e, tr, p := buildSyncTree(t)
	e.svc.cardKinds = []domain.Kind{}
	r, err := e.svc.SyncCards(ctx, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Skipped != len(tr.order()) || r.Created != 0 || len(p.Calls()) != 0 {
		t.Fatalf("%+v", r)
	}
	for _, it := range r.Items {
		if it.Error != syncReasonKindOff {
			t.Errorf("%+v", it)
		}
	}
	e.svc.cardKinds = nil
	if _, err := e.svc.SyncCards(ctx, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	e.svc.taskManager = nil
	title := "Edited"
	if _, err := e.svc.UpdateTask(ctx, tr.tasks[0][0], TaskPatch{Title: &title}); err != nil {
		t.Fatal(err)
	}
	e.svc.taskManager = p
	e.svc.cardKinds = []domain.Kind{}
	r, err = e.svc.SyncCards(ctx, SyncOptions{})
	if err != nil || r.Updated != 1 || r.Skipped != 0 {
		t.Fatalf("%v %+v", err, r)
	}
}
