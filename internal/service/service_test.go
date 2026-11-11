package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	st, err := e.svc.Status(ctx, iID, ListOptions{})
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
	if list, err := e.svc.ListMilestones(ctx, 0, ListOptions{}); err != nil || len(list) != 1 || list[0].HeadCommit != head {
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

func TestArchivedViews(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	iid, mid := e.scaffold(t)
	task, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mid, Title: "T"})
	if err != nil {
		t.Fatal(err)
	}
	tid := mustID(t, domain.KindTask, task.ID)
	inDoc, err := e.svc.CreateDocument(ctx, DocumentInput{InitiativeID: iid, Kind: domain.DocKind("design"), Title: "Scoped"})
	if err != nil {
		t.Fatal(err)
	}
	wide, err := e.svc.CreateDocument(ctx, DocumentInput{Kind: domain.DocKind("design"), Title: "Wide"})
	if err != nil {
		t.Fatal(err)
	}
	wideID := mustID(t, domain.KindDocument, wide.ID)

	fields := func(v any) map[string]any {
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
	want := func(name string, v any, archivedAt, archived bool) {
		t.Helper()
		m := fields(v)
		if _, ok := m["archived_at"]; ok != archivedAt {
			t.Errorf("%s: archived_at present = %v, want %v", name, ok, archivedAt)
		}
		if _, ok := m["archived"]; ok != archived {
			t.Errorf("%s: archived present = %v, want %v", name, ok, archived)
		}
	}
	snapshot := func(archivedInitiative, archivedWide bool) {
		t.Helper()
		in, _ := e.svc.GetInitiative(ctx, iid)
		inList, _ := e.svc.ListInitiatives(ctx, ListOptions{IncludeArchived: true})
		ms, _ := e.svc.GetMilestone(ctx, mid)
		msList, _ := e.svc.ListMilestones(ctx, 0, ListOptions{IncludeArchived: true})
		tk, _ := e.svc.GetTask(ctx, tid)
		tkList, _ := e.svc.ListTasks(ctx, TaskFilter{IncludeArchived: true})
		d, _ := e.svc.GetDocument(ctx, mustID(t, domain.KindDocument, inDoc.ID))
		dList, _ := e.svc.ListDocuments(ctx, DocumentFilter{InitiativeID: iid, IncludeArchived: true})
		w, _ := e.svc.GetDocument(ctx, wideID)
		wList, _ := e.svc.ListDocuments(ctx, DocumentFilter{ProjectOnly: true, IncludeArchived: true})
		want("initiative", in, archivedInitiative, false)
		want("initiative list", inList[0], archivedInitiative, false)
		want("milestone", ms, false, archivedInitiative)
		want("milestone list", msList[0], false, archivedInitiative)
		want("task", tk, false, archivedInitiative)
		want("task list", tkList[0], false, archivedInitiative)
		want("scoped doc", d, false, archivedInitiative)
		want("scoped doc list", dList[0], false, archivedInitiative)
		want("project doc", w, archivedWide, archivedWide)
		want("project doc list", wList[0], archivedWide, archivedWide)
	}

	snapshot(false, false)

	st := e.svc.Store()
	at := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	if err := st.SetInitiativeArchived(ctx, iid, at); err != nil {
		t.Fatal(err)
	}
	if err := st.SetDocumentArchived(ctx, wideID, at); err != nil {
		t.Fatal(err)
	}
	snapshot(true, true)

	in, _ := e.svc.GetInitiative(ctx, iid)
	if in.ArchivedAt != "2026-05-06T07:08:09Z" {
		t.Errorf("initiative archived_at = %q", in.ArchivedAt)
	}
}

func importStatus(t *testing.T, e env, p string) ImportView {
	t.Helper()
	v, err := e.svc.GetImport(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func queueOne(t *testing.T, e env, p, content string) string {
	t.Helper()
	out, err := e.svc.QueueImports(context.Background(), []ImportFile{{Path: p, Content: content}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Path != p {
		t.Fatalf("outcomes = %+v", out)
	}
	return out[0].Outcome
}

func TestQueueImports(t *testing.T) {
	e := newEnv(t)
	if got := queueOne(t, e, "docs/a.md", "one"); got != "new" {
		t.Fatalf("outcome = %s", got)
	}
	if v := importStatus(t, e, "docs/a.md"); v.Status != "pending" || v.Size != 3 || v.Changed {
		t.Fatalf("view = %+v", v)
	}
	if got := queueOne(t, e, "docs/a.md", "one"); got != "queued" {
		t.Fatalf("requeue outcome = %s", got)
	}
}

func TestImportDoneAndChanged(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	queueOne(t, e, "a.md", "one")
	doc, err := e.svc.CreateDocument(ctx, DocumentInput{Title: "Doc", Kind: domain.DocReference, Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	links := []ImportLinkInput{{Document: doc.ID, Relation: domain.ImportCreated}}
	done, err := e.svc.ResolveImport(ctx, "a.md", domain.ImportDone, "", links)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "done" || done.ProcessedAt == "" || done.Changed || len(done.Links) != 1 {
		t.Fatalf("done view = %+v", done)
	}
	im, err := e.svc.Store().GetImportByPath(ctx, "a.md")
	if err != nil || im.LastDoneHash != im.ContentHash || im.LastDoneHash == "" {
		t.Fatalf("last done hash = %q, err %v", im.LastDoneHash, err)
	}
	if got := queueOne(t, e, "a.md", "one"); got != "unchanged" {
		t.Fatalf("outcome = %s", got)
	}
	if got := queueOne(t, e, "a.md", "two"); got != "changed" {
		t.Fatalf("outcome = %s", got)
	}
	v := importStatus(t, e, "a.md")
	if v.Status != "pending" || !v.Changed || v.Note != "" || v.ProcessedAt != "" || len(v.Links) != 1 ||
		v.ContentHash == done.ContentHash || v.Content != "two" {
		t.Fatalf("changed view = %+v", v)
	}
}

func TestImportInvalidPaths(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"../x", "/abs", "a/../b", `a\b`, "", ".", "a//b", "a/", "./a"} {
		_, err := e.svc.QueueImports(context.Background(), []ImportFile{{Path: p, Content: "x"}})
		if !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("path %q: err = %v", p, err)
		}
	}
}

func TestQueueImportsAtomic(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.QueueImports(context.Background(), []ImportFile{{Path: "ok.md", Content: "x"}, {Path: "../bad", Content: "y"}})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
	if list, _ := e.svc.ListImports(context.Background(), nil); len(list) != 0 {
		t.Fatalf("list = %+v", list)
	}
}

func TestResolveImport(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	queueOne(t, e, "a.md", "one")

	if _, err := e.svc.ResolveImport(ctx, "a.md", domain.ImportDeferred, " ", nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("deferred without note: %v", err)
	}
	if _, err := e.svc.ResolveImport(ctx, "a.md", domain.ImportSkipped, "", nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("skipped without note: %v", err)
	}
	if _, err := e.svc.ResolveImport(ctx, "a.md", domain.ImportPending, "n", nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("resolve to pending: %v", err)
	}
	v, err := e.svc.ResolveImport(ctx, "a.md", domain.ImportDeferred, "open items", nil)
	if err != nil || v.Status != "deferred" || v.Note != "open items" || v.ProcessedAt != "" {
		t.Fatalf("deferred: %+v, %v", v, err)
	}

	doc, err := e.svc.CreateDocument(ctx, DocumentInput{Title: "Doc", Kind: domain.DocReference, Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ResolveImport(ctx, "a.md", domain.ImportDone, "",
		[]ImportLinkInput{{Document: doc.ID, Relation: domain.ImportCreated}, {Document: "D-999", Relation: domain.ImportUpdated}}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown document: %v", err)
	}
	if v := importStatus(t, e, "a.md"); v.Status != "deferred" || len(v.Links) != 0 {
		t.Fatalf("after failed resolve: %+v", v)
	}

	link := ImportLinkInput{Document: doc.ID, Relation: domain.ImportCreated}
	v, err = e.svc.ResolveImport(ctx, "a.md", domain.ImportDone, "", []ImportLinkInput{link, link})
	if err != nil || v.Status != "done" || v.ProcessedAt == "" || len(v.Links) != 1 {
		t.Fatalf("done: %+v, %v", v, err)
	}
	_, err = e.svc.ResolveImport(ctx, "a.md", domain.ImportDone, "", nil)
	if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), "done") {
		t.Fatalf("re-resolve: %v", err)
	}
	if _, err := e.svc.ResolveImport(ctx, "missing.md", domain.ImportDone, "", nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestImportViews(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	queueOne(t, e, "a.md", "alpha")
	queueOne(t, e, "b.md", "beta")
	if _, err := e.svc.ResolveImport(ctx, "b.md", domain.ImportSkipped, "not needed", nil); err != nil {
		t.Fatal(err)
	}
	if v := importStatus(t, e, "a.md"); v.Content != "alpha" {
		t.Fatalf("get content = %q", v.Content)
	}
	all, err := e.svc.ListImports(ctx, nil)
	if err != nil || len(all) != 2 {
		t.Fatalf("list: %+v, %v", all, err)
	}
	for _, v := range all {
		if v.Content != "" {
			t.Fatalf("list view has content: %+v", v)
		}
	}
	skipped, err := e.svc.ListImports(ctx, []domain.ImportStatus{domain.ImportSkipped})
	if err != nil || len(skipped) != 1 || skipped[0].Path != "b.md" {
		t.Fatalf("filtered: %+v, %v", skipped, err)
	}
	raw, _ := json.Marshal(all[0])
	if strings.Contains(string(raw), `"content"`) {
		t.Fatalf("json has content: %s", raw)
	}
}

func TestImportAsNew(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ini, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "Docs", Brief: "b"})
	if err != nil {
		t.Fatal(err)
	}
	iid, _ := domain.ParseID(domain.KindInitiative, ini.ID)
	md := "intro\n```\n# not a title\n```\n# Heading\nbody\n"
	files := []ImportFile{{Path: "docs/a.md", Content: md}, {Path: "docs/notes.txt", Content: "plain text"}, {Path: "cfg/app.json", Content: `{"a":1}`}}
	out, err := e.svc.ImportAsNew(ctx, files, AsNewOptions{Kind: domain.DocDesign, InitiativeID: iid})
	if err != nil {
		t.Fatal(err)
	}
	for i, o := range out {
		if o.Outcome != AsNewCreated || o.Document == "" {
			t.Fatalf("outcome %d = %+v", i, o)
		}
	}
	for i, want := range []string{"Heading", "notes", "app"} {
		d, err := e.svc.GetDocument(ctx, int64(i+1))
		if err != nil {
			t.Fatal(err)
		}
		if d.Title != want || d.Kind != "design" || d.InitiativeID != ini.ID || d.Body != strings.TrimSpace(files[i].Content) {
			t.Errorf("doc %d = %+v", i, d)
		}
	}
	v := importStatus(t, e, "docs/a.md")
	if v.Status != "done" || len(v.Links) != 1 || v.Links[0].Relation != "created" {
		t.Fatalf("record = %+v", v)
	}

	out, _ = e.svc.ImportAsNew(ctx, files[:1], AsNewOptions{Kind: domain.DocDesign, InitiativeID: iid})
	if out[0].Outcome != AsNewSkipped || out[0].Reason != "already imported" {
		t.Fatalf("rerun = %+v", out[0])
	}
	changed := []ImportFile{{Path: "docs/a.md", Content: md + "more\n"}}
	out, _ = e.svc.ImportAsNew(ctx, changed, AsNewOptions{Kind: domain.DocDesign, InitiativeID: iid})
	if out[0].Outcome != AsNewRefused || out[0].Reason != AsNewChangedHint {
		t.Fatalf("changed = %+v", out[0])
	}
	out, _ = e.svc.ImportAsNew(ctx, changed, AsNewOptions{Kind: domain.DocDesign, InitiativeID: iid, Again: true})
	if out[0].Outcome != AsNewCreated {
		t.Fatalf("again = %+v", out[0])
	}
	v = importStatus(t, e, "docs/a.md")
	if v.Status != "done" || len(v.Links) != 2 {
		t.Fatalf("record after again = %+v", v)
	}
	if docs, _ := e.svc.ListDocuments(ctx, DocumentFilter{}); len(docs) != 4 {
		t.Fatalf("documents = %d", len(docs))
	}
}

func TestImportAsNewUpFrontErrors(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := []ImportFile{{Path: "a.md", Content: "# A"}}
	if _, err := e.svc.ImportAsNew(ctx, f, AsNewOptions{Kind: "bogus"}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("kind err = %v", err)
	}
	if _, err := e.svc.ImportAsNew(ctx, f, AsNewOptions{Kind: domain.DocReference, InitiativeID: 99}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("initiative err = %v", err)
	}
	ini, _ := e.svc.CreateInitiative(ctx, InitiativeInput{Title: "X", Brief: "b"})
	if _, err := e.svc.ArchiveInitiative(ctx, 1, DocumentChoice{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ImportAsNew(ctx, f, AsNewOptions{Kind: domain.DocReference, InitiativeID: 1}); err == nil {
		t.Errorf("archived initiative %s accepted", ini.ID)
	}
	if list, _ := e.svc.ListImports(ctx, nil); len(list) != 0 {
		t.Errorf("records = %+v", list)
	}
}

func TestImportAsNewIsolatesFailures(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	files := []ImportFile{{Path: "a.md", Content: "# A"}, {Path: "docs/.md", Content: "no title"}, {Path: "c.md", Content: "# C"}}
	out, err := e.svc.ImportAsNew(ctx, files, AsNewOptions{Kind: domain.DocReference})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Outcome != AsNewCreated || out[1].Outcome != AsNewFailed || out[1].Reason == "" || out[2].Outcome != AsNewCreated {
		t.Fatalf("out = %+v", out)
	}
	if list, _ := e.svc.ListImports(ctx, nil); len(list) != 2 {
		t.Fatalf("records = %+v", list)
	}
}

func TestImportTask(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, mID := e.scaffold(t)

	for _, st := range []domain.Status{domain.StatusProposed, domain.StatusTodo, domain.StatusBlocked, domain.StatusDone, domain.StatusCancelled} {
		in := ImportTaskInput{
			MilestoneID: mID, Title: "Task " + string(st), Description: "d", AcceptanceCriteria: "ac",
			Status: st, Reason: "because " + string(st),
		}
		v, err := e.svc.ImportTask(ctx, in)
		if err != nil {
			t.Fatalf("%s: %v", st, err)
		}
		if v.Status != string(st) || v.FailureCount != 0 {
			t.Errorf("%s: view = %+v", st, v)
		}
		want := "Imported as " + string(st) + ": because " + string(st)
		if len(v.Notes) != 1 || v.Notes[0].Kind != "NOTE" || v.Notes[0].Body != want {
			t.Errorf("%s: notes = %+v, want %q", st, v.Notes, want)
		}
		if st == domain.StatusBlocked && v.BlockedReason != "because BLOCKED" {
			t.Errorf("blocked reason = %q", v.BlockedReason)
		}
		if st == domain.StatusDone && len(v.AllowedNext) != 0 {
			t.Errorf("DONE allowed next = %v", v.AllowedNext)
		}
	}

	v, err := e.svc.ImportTask(ctx, ImportTaskInput{
		MilestoneID: mID, Title: "Sourced", Status: domain.StatusProposed, Reason: "unsure", Source: "TRACKER.md",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Notes) != 1 || v.Notes[0].Body != "Imported as PROPOSED: unsure (source: TRACKER.md)" {
		t.Errorf("notes = %+v", v.Notes)
	}

	next, err := e.svc.NextTask(ctx, mID)
	if err != nil || next == nil || next.Status != "TODO" {
		t.Errorf("NextTask = %+v, %v", next, err)
	}
}

func TestImportTaskRejections(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, mID := e.scaffold(t)

	cases := map[string]ImportTaskInput{
		"todo without criteria": {MilestoneID: mID, Title: "t", Description: "d", Status: domain.StatusTodo, Reason: "r"},
		"todo without desc":     {MilestoneID: mID, Title: "t", AcceptanceCriteria: "ac", Status: domain.StatusTodo, Reason: "r"},
		"in progress":           {MilestoneID: mID, Title: "t", Status: domain.StatusInProgress, Reason: "r"},
		"unknown status":        {MilestoneID: mID, Title: "t", Status: "WHATEVER", Reason: "r"},
		"empty reason":          {MilestoneID: mID, Title: "t", Status: domain.StatusProposed, Reason: "  "},
		"blocked no reason":     {MilestoneID: mID, Title: "t", Status: domain.StatusBlocked},
		"empty title":           {MilestoneID: mID, Title: " ", Status: domain.StatusProposed, Reason: "r"},
		"negative position":     {MilestoneID: mID, Title: "t", Status: domain.StatusProposed, Reason: "r", Position: -1},
	}
	for name, in := range cases {
		if _, err := e.svc.ImportTask(ctx, in); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := e.svc.ImportTask(ctx, ImportTaskInput{MilestoneID: 99, Title: "t", Status: domain.StatusProposed, Reason: "r"}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing milestone: %v", err)
	}
	if tasks, err := e.svc.ListTasks(ctx, TaskFilter{MilestoneID: mID}); err != nil || len(tasks) != 0 {
		t.Errorf("tasks after failed imports = %+v, %v", tasks, err)
	}
}

func TestImportTaskLeavesTransitionsAlone(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, mID := e.scaffold(t)
	tv, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mID, Title: "t"})
	if err != nil || tv.Status != "PROPOSED" {
		t.Fatalf("CreateTask = %+v, %v", tv, err)
	}
	if _, err := e.svc.SetTaskStatus(ctx, mustID(t, domain.KindTask, tv.ID), domain.StatusDone, "x"); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("PROPOSED → DONE: %v", err)
	}
}

func TestImportTaskArchivedInitiative(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	iID, mID := e.scaffold(t)
	if _, err := e.svc.ArchiveInitiative(ctx, iID, DocumentChoice{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ImportTask(ctx, ImportTaskInput{MilestoneID: mID, Title: "t", Status: domain.StatusProposed, Reason: "r"}); !errors.Is(err, ErrArchived) {
		t.Errorf("archived import: %v", err)
	}
}
