package service

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/search"
	"github.com/bengearig/ralph/internal/store"
	"github.com/bengearig/ralph/internal/store/sqlite"
)

type hidingFixture struct {
	e                      env
	archivedIni, activeIni int64
	archived, visible      []string
	projectDoc             string
}

func newHidingFixture(t *testing.T, e env) hidingFixture {
	t.Helper()
	ctx := context.Background()
	f := hidingFixture{e: e}
	mk := func(title string) (int64, string, string, string) {
		i, err := e.svc.CreateInitiative(ctx, InitiativeInput{Title: title, Brief: "zebra brief"})
		if err != nil {
			t.Fatal(err)
		}
		iID := mustID(t, domain.KindInitiative, i.ID)
		m, err := e.svc.CreateMilestone(ctx, MilestoneInput{InitiativeID: iID, Title: title + " milestone zebra"})
		if err != nil {
			t.Fatal(err)
		}
		task, err := e.svc.CreateTask(ctx, TaskInput{MilestoneID: mustID(t, domain.KindMilestone, m.ID), Title: title + " task zebra"})
		if err != nil {
			t.Fatal(err)
		}
		d, err := e.svc.CreateDocument(ctx, DocumentInput{InitiativeID: iID, Kind: domain.DocDesign, Title: title + " doc", Body: "zebra body"})
		if err != nil {
			t.Fatal(err)
		}
		return iID, m.ID, task.ID, d.ID
	}
	var m, task, d string
	f.archivedIni, m, task, d = mk("Gone")
	f.archived = []string{domain.FormatID(domain.KindInitiative, f.archivedIni), m, task, d}
	var vm, vt, vd string
	f.activeIni, vm, vt, vd = mk("Live")
	f.visible = []string{domain.FormatID(domain.KindInitiative, f.activeIni), vm, vt, vd}
	pd, err := e.svc.CreateDocument(ctx, DocumentInput{Kind: domain.DocReference, Title: "Old reference", Body: "zebra reference"})
	if err != nil {
		t.Fatal(err)
	}
	f.projectDoc = pd.ID
	f.archived = append(f.archived, pd.ID)
	if _, err := e.svc.ArchiveInitiative(ctx, f.archivedIni, DocumentChoice{None: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ArchiveDocument(ctx, mustID(t, domain.KindDocument, pd.ID)); err != nil {
		t.Fatal(err)
	}
	return f
}

func sorted(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

func (f hidingFixture) checkIDs(t *testing.T, what string, got []string, wantIncluded, wantHidden []string) {
	t.Helper()
	for _, id := range wantIncluded {
		if !slices.Contains(got, id) {
			t.Errorf("%s: missing %s in %v", what, id, got)
		}
	}
	for _, id := range wantHidden {
		if slices.Contains(got, id) {
			t.Errorf("%s: %s should be hidden; got %v", what, id, got)
		}
	}
}

func TestListsHideArchived(t *testing.T) {
	f := newHidingFixture(t, newEnv(t))
	ctx := context.Background()
	svc := f.e.svc
	archivedOf := func(kind domain.Kind) []string {
		var out []string
		for _, id := range f.archived {
			if len(id) > 1 && id[0] == domain.FormatID(kind, 0)[0] {
				out = append(out, id)
			}
		}
		return out
	}
	visibleOf := func(kind domain.Kind) []string {
		var out []string
		for _, id := range f.visible {
			if id[0] == domain.FormatID(kind, 0)[0] {
				out = append(out, id)
			}
		}
		return out
	}

	for _, include := range []bool{false, true} {
		opts := ListOptions{IncludeArchived: include}
		inits, err := svc.ListInitiatives(ctx, opts)
		if err != nil {
			t.Fatal(err)
		}
		ms, err := svc.ListMilestones(ctx, 0, opts)
		if err != nil {
			t.Fatal(err)
		}
		tasks, err := svc.ListTasks(ctx, TaskFilter{IncludeArchived: include})
		if err != nil {
			t.Fatal(err)
		}
		docs, err := svc.ListDocuments(ctx, DocumentFilter{IncludeArchived: include})
		if err != nil {
			t.Fatal(err)
		}
		projectOnly, err := svc.ListDocuments(ctx, DocumentFilter{ProjectOnly: true, IncludeArchived: include})
		if err != nil {
			t.Fatal(err)
		}
		var ids = map[domain.Kind][]string{}
		for _, v := range inits {
			ids[domain.KindInitiative] = append(ids[domain.KindInitiative], v.ID)
		}
		for _, v := range ms {
			ids[domain.KindMilestone] = append(ids[domain.KindMilestone], v.ID)
			if include && v.ID == archivedOf(domain.KindMilestone)[0] && !v.Archived {
				t.Errorf("milestone view not marked archived: %+v", v)
			}
		}
		for _, v := range tasks {
			ids[domain.KindTask] = append(ids[domain.KindTask], v.ID)
			if include && v.ID == archivedOf(domain.KindTask)[0] && !v.Archived {
				t.Errorf("task view not marked archived: %+v", v)
			}
		}
		for _, v := range docs {
			ids[domain.KindDocument] = append(ids[domain.KindDocument], v.ID)
			if include && slices.Contains(f.archived, v.ID) && !v.Archived && v.ArchivedAt == "" {
				t.Errorf("document view not marked archived: %+v", v)
			}
		}
		for _, v := range inits {
			if include && v.ID == archivedOf(domain.KindInitiative)[0] && v.ArchivedAt == "" {
				t.Errorf("initiative view has no archived_at: %+v", v)
			}
		}
		for _, k := range domain.Kinds() {
			hidden := archivedOf(k)
			if include {
				f.checkIDs(t, string(k), ids[k], append(visibleOf(k), hidden...), nil)
			} else {
				f.checkIDs(t, string(k), ids[k], visibleOf(k), hidden)
			}
		}
		var projectIDs []string
		for _, v := range projectOnly {
			projectIDs = append(projectIDs, v.ID)
		}
		if include != slices.Contains(projectIDs, f.projectDoc) {
			t.Errorf("include=%v project-only docs = %v", include, projectIDs)
		}
	}

	if _, err := svc.GetInitiative(ctx, f.archivedIni); err != nil {
		t.Errorf("get archived initiative: %v", err)
	}
	if _, err := svc.GetDocument(ctx, mustID(t, domain.KindDocument, f.projectDoc)); err != nil {
		t.Errorf("get archived document: %v", err)
	}
	if _, err := svc.GetTask(ctx, mustID(t, domain.KindTask, archivedOf(domain.KindTask)[0])); err != nil {
		t.Errorf("get archived task: %v", err)
	}
	if _, err := svc.GetMilestone(ctx, mustID(t, domain.KindMilestone, archivedOf(domain.KindMilestone)[0])); err != nil {
		t.Errorf("get archived milestone: %v", err)
	}
}

func TestStatusHidesArchived(t *testing.T) {
	f := newHidingFixture(t, newEnv(t))
	ctx := context.Background()
	ids := func(opts ListOptions, id int64) []string {
		st, err := f.e.svc.Status(ctx, id, opts)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, i := range st.Initiatives {
			out = append(out, i.ID)
		}
		return out
	}
	archived, active := f.archived[0], f.visible[0]
	if got := ids(ListOptions{}, 0); !reflect.DeepEqual(got, []string{active}) {
		t.Errorf("default status = %v", got)
	}
	if got := ids(ListOptions{IncludeArchived: true}, 0); !reflect.DeepEqual(sorted(got), sorted([]string{active, archived})) {
		t.Errorf("status with archived = %v", got)
	}
	if got := ids(ListOptions{}, f.archivedIni); len(got) != 0 {
		t.Errorf("default status of archived initiative = %v", got)
	}
	if got := ids(ListOptions{IncludeArchived: true}, f.archivedIni); !reflect.DeepEqual(got, []string{archived}) {
		t.Errorf("status of archived initiative with archived = %v", got)
	}
}

func searchIDs(t *testing.T, e env, include bool) []string {
	t.Helper()
	v, err := e.svc.Search(context.Background(), SearchInput{Query: "zebra", Limit: 50, IncludeArchived: include})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, h := range v.Hits {
		out = append(out, h.ID)
	}
	return out
}

func keywordEnv(t *testing.T) env {
	t.Helper()
	st, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ix := &search.Index{Store: st, Chunker: search.Chunker{MaxChars: 1000, OverlapChars: 100}}
	return env{svc: New(st, ix, Options{StallThreshold: 2})}
}

func TestSearchHidesArchived(t *testing.T) {
	for name, e := range map[string]env{"vector": newEnv(t), "keyword": keywordEnv(t)} {
		t.Run(name, func(t *testing.T) {
			f := newHidingFixture(t, e)
			ctx := context.Background()
			v, err := e.svc.Search(ctx, SearchInput{Query: "zebra"})
			if err != nil {
				t.Fatal(err)
			}
			if wantMode := search.ModeKeyword; name == "vector" {
				if v.Mode != search.ModeVector {
					t.Fatalf("mode = %s", v.Mode)
				}
			} else if v.Mode != wantMode {
				t.Fatalf("mode = %s", v.Mode)
			}

			type snapshot struct {
				chunks int
				vecs   [][]float32
			}
			snap := func() snapshot {
				var s snapshot
				hits, err := e.svc.Store().KeywordSearch(ctx, "zebra", store.ChunkFilter{}, 100)
				if err != nil {
					t.Fatal(err)
				}
				s.chunks = len(hits)
				if e.emb != nil {
					cs, err := e.svc.Store().EmbeddedChunks(ctx, e.emb.Model(), store.ChunkFilter{})
					if err != nil {
						t.Fatal(err)
					}
					for _, c := range cs {
						s.vecs = append(s.vecs, c.Embedding)
					}
				}
				return s
			}
			before := snap()
			calls := 0
			if e.emb != nil {
				calls = e.emb.Calls()
			}

			f.checkIDs(t, "default search", searchIDs(t, e, false), f.visible, f.archived)
			f.checkIDs(t, "search with archived", searchIDs(t, e, true), append(slices.Clone(f.visible), f.archived...), nil)

			if _, err := e.svc.UnarchiveInitiative(ctx, f.archivedIni); err != nil {
				t.Fatal(err)
			}
			if _, err := e.svc.UnarchiveDocument(ctx, mustID(t, domain.KindDocument, f.projectDoc)); err != nil {
				t.Fatal(err)
			}
			f.checkIDs(t, "search after unarchive", searchIDs(t, e, false), append(slices.Clone(f.visible), f.archived...), nil)

			if after := snap(); !reflect.DeepEqual(before, after) {
				t.Errorf("chunks changed across archive/unarchive: %d -> %d", before.chunks, after.chunks)
			}
			if e.emb != nil {
				if got := e.emb.Calls() - calls; got > 4 {
					t.Errorf("unarchive re-embedded content: %d extra embed calls (queries only expected)", got)
				}
			}
		})
	}
}
