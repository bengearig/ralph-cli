package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/embed/fake"
	"github.com/bengearig/ralph/internal/search"
	"github.com/bengearig/ralph/internal/store"
	"github.com/bengearig/ralph/internal/store/sqlite"
)

type env struct {
	svc *Service
	emb *fake.Embedder
}

func newEnv(t *testing.T) env {
	t.Helper()
	st, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	emb := fake.New()
	ix := &search.Index{Store: st, Embedder: emb, Chunker: search.Chunker{MaxChars: 1000, OverlapChars: 100}}
	return env{svc: New(st, ix, Options{StallThreshold: 2}), emb: emb}
}

func mustID(t *testing.T, k domain.Kind, s string) int64 {
	t.Helper()
	id, err := domain.ParseID(k, s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (e env) scaffold(t *testing.T) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	i, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Customer Login", Brief: "Business wants SSO login"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: mustID(t, domain.KindInitiative, i.ID), Title: "OAuth provider"})
	if err != nil {
		t.Fatal(err)
	}
	return mustID(t, domain.KindInitiative, i.ID), mustID(t, domain.KindMilestone, m.ID)
}

func TestSlugGeneration(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, _ := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Same Title"})
	b, _ := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Same Title"})
	c, _ := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "!!!"})
	if a.Slug != "same-title" || b.Slug != "same-title-2" || c.Slug != "initiative" {
		t.Errorf("slugs = %q %q %q", a.Slug, b.Slug, c.Slug)
	}
	if _, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "x", Slug: "same-title"}); !errors.Is(err, store.ErrConflict) {
		t.Errorf("explicit duplicate slug: %v", err)
	}
	if _, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "x", Slug: "Bad Slug"}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("invalid slug: %v", err)
	}
	if _, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "  "}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("blank title: %v", err)
	}
}

func TestParentsMustExist(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: 42, Title: "m"}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("milestone: %v", err)
	}
	if _, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: 42, Title: "t"}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("task: %v", err)
	}
	if _, err := e.svc.CreateDocument(ctx, DocumentInput{InitiativeID: 42, Kind: domain.DocDesign, Title: "d"}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("document: %v", err)
	}
	if _, err := e.svc.ListTasks(ctx, TaskFilter{MilestoneID: 42}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("list tasks: %v", err)
	}
}

func TestTaskLifecycle(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, mID := e.scaffold(t)

	tv, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mID, Title: "Add callback endpoint"})
	if err != nil {
		t.Fatal(err)
	}
	if tv.Status != "PROPOSED" {
		t.Fatalf("new task status = %s", tv.Status)
	}
	tID := mustID(t, domain.KindTask, tv.ID)

	if _, err := e.svc.SetTaskStatus(ctx, tID, domain.StatusTodo, ""); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("unarticulated promote: %v", err)
	}
	desc, ac := "Handle /oauth/callback", "Callback exchanges code for token; tests pass"
	if _, err := e.svc.UpdateTask(ctx, tID, TaskPatch{Description: &desc, AcceptanceCriteria: &ac}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetTaskStatus(ctx, tID, domain.StatusTodo, "articulated with user"); err != nil {
		t.Fatal(err)
	}
	empty := ""
	if _, err := e.svc.UpdateTask(ctx, tID, TaskPatch{AcceptanceCriteria: &empty}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("clearing criteria on TODO task: %v", err)
	}

	next, err := e.svc.NextTask(ctx, mID)
	if err != nil || next == nil || next.ID != tv.ID {
		t.Fatalf("NextTask = %+v, %v", next, err)
	}
	if _, err := e.svc.SetTaskStatus(ctx, tID, domain.StatusInProgress, ""); err != nil {
		t.Fatal(err)
	}
	if next, _ := e.svc.NextTask(ctx, mID); next == nil || next.Status != "IN_PROGRESS" {
		t.Errorf("interrupted task should be resumed first: %+v", next)
	}
	done, err := e.svc.SetTaskStatus(ctx, tID, domain.StatusDone, "tests green")
	if err != nil {
		t.Fatal(err)
	}
	if len(done.Notes) != 2 || done.Notes[1].Kind != "ITERATION" || !strings.Contains(done.Notes[1].Body, "IN_PROGRESS → DONE: tests green") {
		t.Errorf("notes = %+v", done.Notes)
	}
	if len(done.AllowedNext) != 0 {
		t.Errorf("DONE allowed next = %v", done.AllowedNext)
	}
	title := "new"
	if _, err := e.svc.UpdateTask(ctx, tID, TaskPatch{Title: &title}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("editing DONE task: %v", err)
	}
	if next, _ := e.svc.NextTask(ctx, mID); next != nil {
		t.Errorf("no loopable tasks expected, got %+v", next)
	}
	mv, _ := e.svc.GetMilestone(ctx, mID)
	if mv.Status != MilestoneDone {
		t.Errorf("milestone status = %s", mv.Status)
	}
}

func TestStallStatute(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, mID := e.scaffold(t)
	tv, _ := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mID, Title: "Flaky", Description: "d", AcceptanceCriteria: "a"})
	id := mustID(t, domain.KindTask, tv.ID)
	if _, err := e.svc.SetTaskStatus(ctx, id, domain.StatusTodo, ""); err != nil {
		t.Fatal(err)
	}

	e.svc.SetTaskStatus(ctx, id, domain.StatusInProgress, "")
	first, err := e.svc.SetTaskStatus(ctx, id, domain.StatusTodo, "tests failed")
	if err != nil || first.Status != "TODO" || first.FailureCount != 1 {
		t.Fatalf("first failure = %+v, %v", first, err)
	}
	e.svc.SetTaskStatus(ctx, id, domain.StatusInProgress, "")
	second, err := e.svc.SetTaskStatus(ctx, id, domain.StatusTodo, "tests failed again")
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != "BLOCKED" || !strings.Contains(second.BlockedReason, "Stall Statute") || !strings.Contains(second.BlockedReason, "tests failed again") {
		t.Errorf("stalled task = %+v", second)
	}
	last := second.Notes[len(second.Notes)-1]
	if last.Kind != "KICKBACK" {
		t.Errorf("stall note kind = %s", last.Kind)
	}

	// Kickback to articulation, then back into the loop.
	if _, err := e.svc.SetTaskStatus(ctx, id, domain.StatusProposed, "needs a smaller scope"); err != nil {
		t.Fatal(err)
	}
	mv, _ := e.svc.GetMilestone(ctx, mID)
	if mv.Status != MilestoneArticulating {
		t.Errorf("milestone status = %s", mv.Status)
	}
}

func TestAddNoteValidation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, mID := e.scaffold(t)
	tv, _ := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mID, Title: "t"})
	id := mustID(t, domain.KindTask, tv.ID)
	if _, err := e.svc.AddTaskNote(ctx, id, domain.NoteGeneral, " "); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("blank note: %v", err)
	}
	if _, err := e.svc.AddTaskNote(ctx, 999, domain.NoteGeneral, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing task: %v", err)
	}
	got, err := e.svc.AddTaskNote(ctx, id, domain.NoteIteration, "tried X")
	if err != nil || len(got.Notes) != 1 {
		t.Errorf("note = %+v, %v", got, err)
	}
}

func TestDocumentsAndSearch(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	iID, mID := e.scaffold(t)
	if _, err := e.svc.CreateDocument(ctx, DocumentInput{Kind: domain.DocArchitecture, Title: "System architecture", Body: "Go monolith with postgres database"}); err != nil {
		t.Fatal(err)
	}
	design, err := e.svc.CreateDocument(ctx, DocumentInput{InitiativeID: iID, Kind: domain.DocDesign, Title: "Login design", Body: "OAuth authorization code flow with PKCE"})
	if err != nil {
		t.Fatal(err)
	}
	if design.InitiativeID == "" || design.Slug != "login-design" {
		t.Errorf("design = %+v", design)
	}
	if _, err := e.svc.CreateDocument(ctx, DocumentInput{Kind: "memo", Title: "x"}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("bad kind: %v", err)
	}
	e.svc.CreateTask(ctx, TaskInput{MilestoneID: mID, Title: "Implement PKCE verifier", Description: "authorization code flow"})

	list, _ := e.svc.ListDocuments(ctx, DocumentFilter{ProjectOnly: true})
	if len(list) != 1 || list[0].Body != "" {
		t.Errorf("project docs = %+v", list)
	}

	res, err := e.svc.Search(ctx, SearchInput{Query: "oauth authorization code flow", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != search.ModeVector || len(res.Hits) == 0 {
		t.Fatalf("search = %+v", res)
	}
	if res.Hits[0].Title == "" || res.Hits[0].ID == "" {
		t.Errorf("hit not enriched: %+v", res.Hits[0])
	}

	scoped, _ := e.svc.Search(ctx, SearchInput{Query: "postgres database", InitiativeID: iID})
	for _, h := range scoped.Hits {
		if h.InitiativeID != domain.FormatID(domain.KindInitiative, iID) {
			t.Errorf("initiative filter leaked %+v", h)
		}
	}
	if _, err := e.svc.Search(ctx, SearchInput{Query: " "}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("blank query: %v", err)
	}

	newBody := "Device code flow instead"
	upd, err := e.svc.UpdateDocument(ctx, mustID(t, domain.KindDocument, design.ID), DocumentPatch{Body: &newBody})
	if err != nil || upd.Body != newBody {
		t.Errorf("update = %+v, %v", upd, err)
	}
}

func TestEmbeddingOutageIsAWarningNotAFailure(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.emb.FailWith(errors.New("ollama down"))
	iv, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Offline", Brief: "still saved"})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if !strings.Contains(iv.IndexWarning, "ollama down") {
		t.Errorf("warning = %q", iv.IndexWarning)
	}
	e.emb.FailWith(nil)
	rep, err := e.svc.Reindex(ctx, false, nil)
	if err != nil || rep.Embedded != 1 || rep.Remaining != 0 {
		t.Errorf("reindex = %+v, %v", rep, err)
	}
	rep, err = e.svc.Reindex(ctx, true, nil)
	if err != nil || rep.Rechunked != 1 || rep.Remaining != 0 {
		t.Errorf("rebuild = %+v, %v", rep, err)
	}
}

func TestStatusAndMilestoneDerivation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	iID, mID := e.scaffold(t)
	e.svc.CreateTask(ctx, TaskInput{MilestoneID: mID, Title: "a"})
	e.svc.CreateDocument(ctx, DocumentInput{InitiativeID: iID, Kind: domain.DocBrief, Title: "Brief"})
	st, err := e.svc.Status(ctx, iID)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Initiatives) != 1 || st.Initiatives[0].TaskCounts["PROPOSED"] != 1 || st.Initiatives[0].Documents != 1 {
		t.Errorf("status = %+v", st)
	}
	if FormatCounts(st.Initiatives[0].TaskCounts) != "PROPOSED 1" || FormatCounts(nil) != "no tasks" {
		t.Error("FormatCounts wrong")
	}

	mk := func(ss ...domain.Status) []domain.Task {
		var ts []domain.Task
		for _, s := range ss {
			ts = append(ts, domain.Task{Status: s})
		}
		return ts
	}
	cases := []struct {
		tasks []domain.Task
		want  string
	}{
		{nil, MilestoneEmpty},
		{mk(domain.StatusCancelled), MilestoneCancelled},
		{mk(domain.StatusDone, domain.StatusCancelled), MilestoneDone},
		{mk(domain.StatusTodo, domain.StatusBlocked), MilestoneBlocked},
		{mk(domain.StatusDone, domain.StatusTodo), MilestoneInProgress},
		{mk(domain.StatusProposed, domain.StatusTodo), MilestoneArticulating},
		{mk(domain.StatusTodo, domain.StatusTodo), MilestoneReady},
	}
	for _, c := range cases {
		if got := MilestoneStatus(c.tasks); got != c.want {
			t.Errorf("MilestoneStatus(%v) = %s, want %s", c.tasks, got, c.want)
		}
	}
}

func TestNewEmbedderFromConfig(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	emb, err := NewEmbedder(cfg.Embeddings)
	if err != nil {
		t.Fatal(err)
	}
	if emb.Model() != `ollama/nomic-embed-text document_prefix="search_document: "` {
		t.Errorf("default model identity = %q", emb.Model())
	}
	cfg.Embeddings.Ollama.DocumentPrefix = ""
	emb, _ = NewEmbedder(cfg.Embeddings)
	if emb.Model() != "ollama/nomic-embed-text" {
		t.Errorf("explicitly empty prefix = %q", emb.Model())
	}
	cfg.Embeddings.Provider = config.EmbeddingsNone
	if emb, err := NewEmbedder(cfg.Embeddings); emb != nil || err != nil {
		t.Errorf("none = %v, %v", emb, err)
	}
}

func TestRecordGitState(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, mid := e.scaffold(t)
	tv, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mid, Title: "Wire callback"})
	if err != nil {
		t.Fatal(err)
	}
	tid := mustID(t, domain.KindTask, tv.ID)
	base, head, next := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 64)

	for name, g := range map[string]domain.MilestoneGit{
		"short base":          {Branch: "ralph/x", BaseCommit: "abc1234"},
		"short head":          {Branch: "ralph/x", HeadCommit: "abc1234"},
		"base without branch": {BaseCommit: base},
		"head without branch": {HeadCommit: head},
		"blank branch":        {Branch: "  ", BaseCommit: base, HeadCommit: head},
	} {
		if err := e.svc.RecordMilestoneGit(ctx, mid, g); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("RecordMilestoneGit(%s) = %v, want ErrInvalid", name, err)
		}
	}
	if err := e.svc.RecordMilestoneHead(ctx, mid, head); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("RecordMilestoneHead with no branch = %v, want ErrInvalid", err)
	}
	if err := e.svc.RecordMilestoneGit(ctx, 999, domain.MilestoneGit{Branch: "b"}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RecordMilestoneGit(999) = %v, want ErrNotFound", err)
	}

	before, err := e.svc.GetMilestone(ctx, mid)
	if err != nil {
		t.Fatal(err)
	}
	g := domain.MilestoneGit{Branch: "ralph/oauth-provider", BaseCommit: base, HeadCommit: base}
	if err := e.svc.RecordMilestoneGit(ctx, mid, g); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RecordMilestoneHead(ctx, mid, head); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RecordMilestoneHead(ctx, mid, "nothex"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("RecordMilestoneHead(nothex) = %v, want ErrInvalid", err)
	}
	if err := e.svc.RecordMilestoneHead(ctx, 999, head); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RecordMilestoneHead(999) = %v, want ErrNotFound", err)
	}
	mv, err := e.svc.GetMilestone(ctx, mid)
	if err != nil {
		t.Fatal(err)
	}
	if mv.Branch != g.Branch || mv.BaseCommit != base || mv.HeadCommit != head {
		t.Errorf("milestone view = branch %q base %q head %q", mv.Branch, mv.BaseCommit, mv.HeadCommit)
	}
	if mv.UpdatedAt != before.UpdatedAt || mv.Title != before.Title || mv.Description != before.Description {
		t.Errorf("recording changed other fields: before %+v, after %+v", before, mv)
	}

	for _, bad := range []string{"abc1234", strings.ToUpper(head), head + "0", "z" + head[1:]} {
		if err := e.svc.RecordTaskCommit(ctx, tid, bad); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("RecordTaskCommit(%q) = %v, want ErrInvalid", bad, err)
		}
	}
	if err := e.svc.RecordTaskCommit(ctx, 999, next); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RecordTaskCommit(999) = %v, want ErrNotFound", err)
	}
	if err := e.svc.RecordTaskCommit(ctx, tid, next); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.GetTask(ctx, tid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Commit != next || got.UpdatedAt != tv.UpdatedAt {
		t.Errorf("task view commit %q updated %s; want %q, %s", got.Commit, got.UpdatedAt, next, tv.UpdatedAt)
	}
	if list, err := e.svc.ListTasks(ctx, TaskFilter{MilestoneID: mid}); err != nil || len(list) != 1 || list[0].Commit != next {
		t.Errorf("ListTasks = %+v, %v", list, err)
	}
	if list, err := e.svc.ListMilestones(ctx, 0); err != nil || len(list) != 1 || list[0].HeadCommit != head {
		t.Errorf("ListMilestones = %+v, %v", list, err)
	}

	keys := func(v any) map[string]any {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	mj, tj := keys(mv), keys(got)
	if mj["branch"] != g.Branch || mj["base_commit"] != base || mj["head_commit"] != head {
		t.Errorf("milestone JSON = %v", mj)
	}
	if tj["commit"] != next {
		t.Errorf("task JSON = %v", tj)
	}

	if err := e.svc.RecordMilestoneGit(ctx, mid, domain.MilestoneGit{}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RecordTaskCommit(ctx, tid, ""); err != nil {
		t.Fatal(err)
	}
	mv, _ = e.svc.GetMilestone(ctx, mid)
	got, _ = e.svc.GetTask(ctx, tid)
	mj, tj = keys(mv), keys(got)
	for _, k := range []string{"branch", "base_commit", "head_commit"} {
		if _, ok := mj[k]; ok {
			t.Errorf("empty milestone JSON has %q: %v", k, mj)
		}
	}
	if _, ok := tj["commit"]; ok {
		t.Errorf("empty task JSON has commit: %v", tj)
	}
}

func TestReopenTask(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, mid := e.scaffold(t)
	commit := strings.Repeat("d", 40)

	newTask := func(path ...domain.Status) int64 {
		t.Helper()
		desc, ac := "Do the thing", "The thing is done"
		tv, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mid, Title: "Task", Description: desc, AcceptanceCriteria: ac})
		if err != nil {
			t.Fatal(err)
		}
		id := mustID(t, domain.KindTask, tv.ID)
		for _, st := range path {
			if _, err := e.svc.SetTaskStatus(ctx, id, st, "step"); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	snapshot := func(id int64) string {
		t.Helper()
		v, err := e.svc.GetTask(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	toDone := []domain.Status{domain.StatusTodo, domain.StatusInProgress, domain.StatusDone}

	done := newTask(toDone...)
	if err := e.svc.RecordTaskCommit(ctx, done, commit); err != nil {
		t.Fatal(err)
	}
	if v, _ := e.svc.GetTask(ctx, done); v.Commit != commit {
		t.Fatalf("commit not recorded: %q", v.Commit)
	}

	if _, err := e.svc.SetTaskStatus(ctx, done, domain.StatusTodo, "try"); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("SetTaskStatus(DONE → TODO) = %v, want ErrInvalidTransition", err)
	}
	if v, _ := e.svc.GetTask(ctx, done); len(v.AllowedNext) != 0 {
		t.Errorf("DONE allowed next = %v", v.AllowedNext)
	}

	for _, reason := range []string{"", "   \t\n"} {
		before := snapshot(done)
		if _, err := e.svc.ReopenTask(ctx, done, reason); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("ReopenTask(reason %q) = %v, want ErrInvalid", reason, err)
		}
		if after := snapshot(done); after != before {
			t.Errorf("blank reason changed the task:\nbefore %s\nafter  %s", before, after)
		}
	}

	for name, id := range map[string]int64{
		"TODO":        newTask(domain.StatusTodo),
		"IN_PROGRESS": newTask(domain.StatusTodo, domain.StatusInProgress),
		"CANCELLED":   newTask(domain.StatusTodo, domain.StatusCancelled),
	} {
		before := snapshot(id)
		_, err := e.svc.ReopenTask(ctx, id, "lost")
		if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), name) {
			t.Errorf("ReopenTask(%s) = %v, want ErrInvalid naming the status", name, err)
		}
		if after := snapshot(id); after != before {
			t.Errorf("reopening %s changed the task:\nbefore %s\nafter  %s", name, before, after)
		}
	}

	if _, err := e.svc.ReopenTask(ctx, 999, "lost"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ReopenTask(999) = %v, want ErrNotFound", err)
	}

	got, err := e.svc.ReopenTask(ctx, done, "  commit lost in a rebase  ")
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := e.svc.GetTask(ctx, done)
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]TaskView{"returned": got, "persisted": persisted} {
		if v.Status != "TODO" || v.FailureCount != 0 || v.Commit != "" || v.BlockedReason != "" {
			t.Errorf("%s view = status %s failures %d commit %q blocked %q", name, v.Status, v.FailureCount, v.Commit, v.BlockedReason)
		}
		if len(v.Notes) == 0 {
			t.Errorf("%s view has no notes", name)
			continue
		}
		last := v.Notes[len(v.Notes)-1]
		if last.Kind != "NOTE" || last.Body != "Reopened by reconcile: commit lost in a rebase" {
			t.Errorf("%s last note = %+v", name, last)
		}
	}
	if len(persisted.AllowedNext) == 0 {
		t.Errorf("reopened task should be loopable again, allowed next = %v", persisted.AllowedNext)
	}
	if next, err := e.svc.NextTask(ctx, mid); err != nil || next == nil {
		t.Errorf("NextTask after reopen = %+v, %v", next, err)
	}

	if _, err := e.svc.ReopenTask(ctx, done, "again"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("reopening a TODO task again = %v, want ErrInvalid", err)
	}
}

func TestInTx(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, mID := e.scaffold(t)
	tv, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mID, Title: "Route"})
	if err != nil {
		t.Fatal(err)
	}
	id := mustID(t, domain.KindTask, tv.ID)
	commit := strings.Repeat("ab", 20)
	g := domain.MilestoneGit{Branch: "ralph/oauth", HeadCommit: commit}

	boom := errors.New("boom")
	err = e.svc.InTx(ctx, func(tx *Service) error {
		if err := tx.RecordMilestoneGit(ctx, mID, g); err != nil {
			return err
		}
		if err := tx.RecordTaskCommit(ctx, id, commit); err != nil {
			return err
		}
		if _, err := tx.AddTaskNote(ctx, id, domain.NoteGeneral, "inside"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("InTx = %v, want boom", err)
	}
	m, _ := e.svc.GetMilestone(ctx, mID)
	got, _ := e.svc.GetTask(ctx, id)
	if m.Branch != "" || m.HeadCommit != "" || got.Commit != "" || len(got.Notes) != 0 {
		t.Errorf("rolled-back writes persisted: milestone %+v, task %+v", m, got)
	}

	if err := e.svc.InTx(ctx, func(tx *Service) error {
		if err := tx.RecordMilestoneGit(ctx, mID, g); err != nil {
			return err
		}
		return tx.RecordTaskCommit(ctx, id, commit)
	}); err != nil {
		t.Fatal(err)
	}
	m, _ = e.svc.GetMilestone(ctx, mID)
	got, _ = e.svc.GetTask(ctx, id)
	if m.Branch != g.Branch || m.HeadCommit != commit || got.Commit != commit {
		t.Errorf("committed writes missing: milestone %+v, task %+v", m, got)
	}
}
