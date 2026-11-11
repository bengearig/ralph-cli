package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/store"
)

func (e env) docIn(t *testing.T, initiative int64, title string) DocumentView {
	t.Helper()
	v, err := e.svc.CreateDocument(context.Background(), DocumentInput{InitiativeID: initiative, Kind: domain.DocDesign, Title: title, Body: "body"})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// chunkInitiatives returns the initiative ID of each chunk matching word ("" for project-wide).
func (e env) chunkInitiatives(t *testing.T, word string) []string {
	t.Helper()
	hits, err := e.svc.Store().KeywordSearch(context.Background(), word, store.ChunkFilter{}, 50)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, h := range hits {
		ref := ""
		if h.Chunk.InitiativeID != 0 {
			ref = domain.FormatID(domain.KindInitiative, h.Chunk.InitiativeID)
		}
		out = append(out, ref)
	}
	return out
}

func TestPromoteAndReturnDocument(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ini, _ := e.scaffold(t)
	iniRef := domain.FormatID(domain.KindInitiative, ini)
	id := e.doc(t, ini, "zebrafish notes")

	if got := e.chunkInitiatives(t, "zebrafish"); len(got) != 1 || got[0] != iniRef {
		t.Fatalf("chunks before = %v", got)
	}
	v, err := e.svc.PromoteDocument(ctx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	if v.OriginInitiativeID != iniRef || v.InitiativeID != "" {
		t.Errorf("view = %+v", v)
	}
	if got := e.chunkInitiatives(t, "zebrafish"); len(got) != 1 || got[0] != "" {
		t.Errorf("chunks after promote = %v", got)
	}
	inIni, _ := e.svc.ListDocuments(ctx, DocumentFilter{InitiativeID: ini})
	if len(inIni) != 0 {
		t.Errorf("initiative list = %+v", inIni)
	}
	proj, _ := e.svc.ListDocuments(ctx, DocumentFilter{ProjectOnly: true})
	if len(proj) != 1 || proj[0].ID != v.ID {
		t.Errorf("project list = %+v", proj)
	}
	if _, err := e.svc.PromoteDocument(ctx, id, ""); !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), "already project-wide") {
		t.Errorf("promote twice: %v", err)
	}

	r, err := e.svc.ReturnDocument(ctx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.InitiativeID != iniRef || r.OriginInitiativeID != "" {
		t.Errorf("returned view = %+v", r)
	}
	if got := e.chunkInitiatives(t, "zebrafish"); len(got) != 1 || got[0] != iniRef {
		t.Errorf("chunks after return = %v", got)
	}
	if _, err := e.svc.ReturnDocument(ctx, id, ""); !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), "no origin initiative") {
		t.Errorf("return without origin: %v", err)
	}
}

func TestPromoteProjectWideDocument(t *testing.T) {
	e := newEnv(t)
	id := e.doc(t, 0, "body")
	if _, err := e.svc.PromoteDocument(context.Background(), id, ""); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("promote project-wide: %v", err)
	}
}

func TestPromoteArchived(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ini, _ := e.scaffold(t)
	archivedDoc := e.doc(t, ini, "a")
	inArchivedInit := e.doc(t, ini, "b")
	if _, err := e.svc.ArchiveDocument(ctx, archivedDoc); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.PromoteDocument(ctx, archivedDoc, ""); !errors.Is(err, ErrArchived) {
		t.Errorf("promote archived document: %v", err)
	}
	if _, err := e.svc.ArchiveInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	v, err := e.svc.PromoteDocument(ctx, inArchivedInit, "")
	if err != nil || v.InitiativeID != "" || v.OriginInitiativeID == "" {
		t.Errorf("promote from archived initiative: %+v, %v", v, err)
	}
}

func TestPromoteSlugClash(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ini, _ := e.scaffold(t)
	other := e.docIn(t, 0, "Doc")
	otherID := mustID(t, domain.KindDocument, other.ID)
	if _, err := e.svc.ArchiveDocument(ctx, otherID); err != nil {
		t.Fatal(err)
	}
	id := e.doc(t, ini, "body")

	_, err := e.svc.PromoteDocument(ctx, id, "")
	if !errors.Is(err, store.ErrConflict) || !strings.Contains(err.Error(), other.ID) || !strings.Contains(err.Error(), "new slug") {
		t.Fatalf("clash: %v", err)
	}
	if v, _ := e.svc.GetDocument(ctx, id); v.InitiativeID == "" || v.OriginInitiativeID != "" {
		t.Errorf("document changed: %+v", v)
	}

	if _, err := e.svc.PromoteDocument(ctx, id, "Bad Slug"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("invalid slug: %v", err)
	}
	if v, _ := e.svc.GetDocument(ctx, id); v.InitiativeID == "" {
		t.Errorf("invalid slug changed the document: %+v", v)
	}

	v, err := e.svc.PromoteDocument(ctx, id, "fresh")
	if err != nil || v.Slug != "fresh" || v.InitiativeID != "" {
		t.Fatalf("free slug: %+v, %v", v, err)
	}
	if o, _ := e.svc.GetDocument(ctx, otherID); o.Slug != other.Slug || o.Title != other.Title {
		t.Errorf("other document touched: %+v", o)
	}
}

func TestPromoteFreeSlugAppliedWithoutClash(t *testing.T) {
	e := newEnv(t)
	ini, _ := e.scaffold(t)
	id := e.doc(t, ini, "body")
	v, err := e.svc.PromoteDocument(context.Background(), id, "renamed")
	if err != nil || v.Slug != "renamed" {
		t.Errorf("got %+v, %v", v, err)
	}
}

func TestReturnDocumentRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ini, _ := e.scaffold(t)
	id := e.doc(t, ini, "body")
	if _, err := e.svc.PromoteDocument(ctx, id, ""); err != nil {
		t.Fatal(err)
	}
	clash := e.docIn(t, ini, "Doc")

	_, err := e.svc.ReturnDocument(ctx, id, "")
	if !errors.Is(err, store.ErrConflict) || !strings.Contains(err.Error(), clash.ID) {
		t.Fatalf("clash: %v", err)
	}
	if _, err := e.svc.ReturnDocument(ctx, id, "Bad Slug"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("invalid slug: %v", err)
	}
	if v, err := e.svc.ReturnDocument(ctx, id, "back"); err != nil || v.Slug != "back" || v.OriginInitiativeID != "" {
		t.Fatalf("free slug: %+v, %v", v, err)
	}

	id2 := e.doc(t, ini, "body")
	if _, err := e.svc.PromoteDocument(ctx, id2, "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ArchiveDocument(ctx, id2); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ReturnDocument(ctx, id2, ""); !errors.Is(err, ErrArchived) {
		t.Errorf("archived document: %v", err)
	}
	if _, err := e.svc.UnarchiveDocument(ctx, id2); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ArchiveInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ReturnDocument(ctx, id2, ""); !errors.Is(err, ErrArchived) || !strings.Contains(err.Error(), "initiative unarchive") {
		t.Errorf("archived origin: %v", err)
	}
}

func TestReturnDocumentOriginDeleted(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ini, _ := e.scaffold(t)
	id := e.doc(t, ini, "body")
	if _, err := e.svc.PromoteDocument(ctx, id, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DeleteInitiative(ctx, ini, true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ReturnDocument(ctx, id, ""); !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), "no origin initiative") {
		t.Errorf("origin deleted: %v", err)
	}
}

func TestPromoteReturnUnknownDocument(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.svc.PromoteDocument(ctx, 99, ""); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("promote: %v", err)
	}
	if _, err := e.svc.ReturnDocument(ctx, 99, ""); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("return: %v", err)
	}
}
