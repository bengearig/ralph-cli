package search

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/embed"
	"github.com/bengearig/ralph/internal/embed/fake"
	"github.com/bengearig/ralph/internal/store"
	"github.com/bengearig/ralph/internal/store/sqlite"
)

func TestSplitEdgeCases(t *testing.T) {
	c := Chunker{MaxChars: 100, OverlapChars: 10}
	if got := c.Split("", "   "); got != nil {
		t.Errorf("empty = %v", got)
	}
	if got := c.Split("Title", ""); len(got) != 1 || got[0] != "Title" {
		t.Errorf("title only = %v", got)
	}
	if got := c.Split("", "plain text without headings"); len(got) != 1 || got[0] != "plain text without headings" {
		t.Errorf("no headings = %v", got)
	}
	got := c.Split("T", "# A\nshort\n# B\nalso short")
	if len(got) != 1 || !strings.HasPrefix(got[0], "T\n\n# A") || !strings.Contains(got[0], "# B") {
		t.Errorf("small sections should pack together: %q", got)
	}
}

func TestSplitRespectsHeadingsAndFences(t *testing.T) {
	c := Chunker{MaxChars: 60, OverlapChars: 5}
	body := "# One\n" + strings.Repeat("a ", 20) + "\n## Two\n```\n# not a heading\n```\n" + strings.Repeat("b ", 10)
	got := c.Split("", body)
	if len(got) != 2 {
		t.Fatalf("chunks = %q", got)
	}
	if !strings.HasPrefix(got[1], "## Two") || !strings.Contains(got[1], "# not a heading") {
		t.Errorf("fenced heading must not split: %q", got[1])
	}
	if !isHeading("###") || isHeading("#tag") || isHeading("####### seven") {
		t.Error("isHeading wrong")
	}
}

func TestSplitOversizedSectionWindowsWithOverlap(t *testing.T) {
	c := Chunker{MaxChars: 50, OverlapChars: 10}
	var words []string
	for i := range 60 {
		words = append(words, fmt.Sprintf("w%02d", i))
	}
	body := strings.Join(words, " ")
	got := c.Split("Doc", body)
	if len(got) < 3 {
		t.Fatalf("expected windows, got %q", got)
	}
	for i, ch := range got {
		if n := utf8.RuneCountInString(ch); n > 50 {
			t.Errorf("chunk %d has %d runes", i, n)
		}
		if !strings.HasPrefix(ch, "Doc\n\n") {
			t.Errorf("chunk %d lacks title prefix", i)
		}
	}
	for i := 1; i < len(got); i++ {
		prev := strings.Fields(strings.TrimPrefix(got[i-1], "Doc\n\n"))
		cur := strings.Fields(strings.TrimPrefix(got[i], "Doc\n\n"))
		last := prev[len(prev)-1]
		if !strings.Contains(strings.Join(cur, " "), last) && cur[0] <= last {
			t.Errorf("windows %d/%d neither overlap nor advance: %q | %q", i-1, i, last, cur[0])
		}
	}
	joined := strings.Join(got, " ")
	for _, w := range words {
		if !strings.Contains(joined, w) {
			t.Errorf("word %s lost", w)
		}
	}
}

func TestSplitMultibyteAndHugeTitle(t *testing.T) {
	c := Chunker{MaxChars: 20, OverlapChars: 19}
	got := c.Split(strings.Repeat("T", 40), strings.Repeat("é", 70))
	if len(got) == 0 {
		t.Fatal("no chunks")
	}
	for _, ch := range got {
		if !utf8.ValidString(ch) {
			t.Error("split inside a rune")
		}
	}
}

func TestCosine(t *testing.T) {
	if got := Cosine([]float32{1, 0}, []float32{1, 0}); math.Abs(got-1) > 1e-9 {
		t.Errorf("identical = %v", got)
	}
	if got := Cosine([]float32{1, 0}, []float32{0, 1}); got != 0 {
		t.Errorf("orthogonal = %v", got)
	}
	if got := Cosine([]float32{0, 0}, []float32{1, 1}); got != 0 {
		t.Errorf("zero vector = %v", got)
	}
}

type fixture struct {
	st  *sqlite.Store
	emb *fake.Embedder
	ix  *Index
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	st, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	emb := fake.New()
	return fixture{st: st, emb: emb, ix: &Index{Store: st, Embedder: emb, Chunker: Chunker{MaxChars: 500, OverlapChars: 50}}}
}

func (f fixture) item(t *testing.T, kind domain.Kind, title, body string) store.Owner {
	t.Helper()
	ctx := context.Background()
	var id int64
	switch kind {
	case domain.KindDocument:
		d := domain.Document{Kind: domain.DocNote, Slug: domain.Slugify(title), Title: title, Body: body}
		if err := f.st.CreateDocument(ctx, &d); err != nil {
			t.Fatal(err)
		}
		id = d.ID
	case domain.KindInitiative:
		i := domain.Initiative{Slug: domain.Slugify(title), Title: title, Brief: body}
		if err := f.st.CreateInitiative(ctx, &i); err != nil {
			t.Fatal(err)
		}
		id = i.ID
	default:
		t.Fatalf("unsupported kind %s", kind)
	}
	owner := store.Owner{Kind: kind, ID: id}
	if _, err := f.ix.Index(ctx, Item{Owner: owner, Title: title, Body: body}); err != nil {
		t.Fatal(err)
	}
	return owner
}

func TestVectorSearchRanksBestOwnerFirst(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	db := f.item(t, domain.KindDocument, "Database", "postgres schema migrations and indexes")
	auth := f.item(t, domain.KindDocument, "Auth", "oauth login tokens sessions")
	f.item(t, domain.KindInitiative, "Payments", "stripe invoices")

	res, err := f.ix.Search(ctx, Query{Text: "login with oauth tokens", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != ModeVector || len(res.Warnings) != 0 {
		t.Fatalf("mode = %s warnings = %v", res.Mode, res.Warnings)
	}
	if len(res.Hits) != 3 || res.Hits[0].Owner != auth {
		t.Errorf("hits = %+v", res.Hits)
	}

	res, _ = f.ix.Search(ctx, Query{Text: "schema", Kinds: []domain.Kind{domain.KindDocument}, Limit: 1})
	if len(res.Hits) != 1 || res.Hits[0].Owner != db {
		t.Errorf("filtered/limited hits = %+v", res.Hits)
	}
}

func TestEmbeddingFailureLeavesStaleThenBackfills(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.emb.FailWith(fmt.Errorf("%w: down", embed.ErrUnavailable))

	d := domain.Document{Kind: domain.DocNote, Slug: "x", Title: "Auth", Body: "oauth login"}
	if err := f.st.CreateDocument(ctx, &d); err != nil {
		t.Fatal(err)
	}
	owner := store.Owner{Kind: domain.KindDocument, ID: d.ID}
	res, err := f.ix.Index(ctx, Item{Owner: owner, Title: d.Title, Body: d.Body})
	if err != nil {
		t.Fatalf("write must not fail when embedding fails: %v", err)
	}
	if res.Stale != 1 || !strings.Contains(res.Warning, "ralph index") {
		t.Errorf("result = %+v", res)
	}

	sr, err := f.ix.Search(ctx, Query{Text: "oauth"})
	if err != nil {
		t.Fatal(err)
	}
	if sr.Mode != ModeKeyword || len(sr.Hits) != 1 || len(sr.Warnings) == 0 {
		t.Errorf("fallback = %+v", sr)
	}

	if _, err := f.ix.Backfill(ctx, 10, nil); !errors.Is(err, embed.ErrUnavailable) {
		t.Errorf("backfill while down: %v", err)
	}
	f.emb.FailWith(nil)
	var progress []int
	bf, err := f.ix.Backfill(ctx, 10, func(done, _ int) { progress = append(progress, done) })
	if err != nil || bf.Embedded != 1 || bf.Remaining != 0 || len(progress) != 1 {
		t.Errorf("backfill = %+v, %v, %v", bf, err, progress)
	}
	sr, _ = f.ix.Search(ctx, Query{Text: "oauth"})
	if sr.Mode != ModeVector {
		t.Errorf("after backfill mode = %s", sr.Mode)
	}
}

func TestQueryEmbeddingFailureFallsBack(t *testing.T) {
	f := newFixture(t)
	f.item(t, domain.KindDocument, "Auth", "oauth login")
	f.emb.FailWith(errors.New("boom"))
	res, err := f.ix.Search(context.Background(), Query{Text: "oauth"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != ModeKeyword || len(res.Hits) != 1 || !strings.Contains(res.Warnings[0], "boom") {
		t.Errorf("res = %+v", res)
	}
}

func TestReindexUnchangedContentSkipsEmbedding(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner := f.item(t, domain.KindDocument, "Auth", "# A\noauth\n# B\nlogin")
	before := f.emb.Calls()
	if _, err := f.ix.Index(ctx, Item{Owner: owner, Title: "Auth", Body: "# A\noauth\n# B\nlogin"}); err != nil {
		t.Fatal(err)
	}
	if f.emb.Calls() != before {
		t.Error("unchanged content should reuse embeddings")
	}
}

func TestPartialIndexWarns(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.item(t, domain.KindDocument, "Auth", "oauth login")
	f.emb.FailWith(errors.New("down"))
	f.item(t, domain.KindDocument, "Other", "other thing")
	f.emb.FailWith(nil)
	res, err := f.ix.Search(ctx, Query{Text: "oauth"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != ModeVector || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "not embedded") {
		t.Errorf("res = %+v", res)
	}
}

func TestKeywordOnlyWithoutEmbedder(t *testing.T) {
	f := newFixture(t)
	f.ix.Embedder = nil
	f.item(t, domain.KindDocument, "Auth", "oauth login")
	res, err := f.ix.Search(context.Background(), Query{Text: "oauth"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != ModeKeyword || len(res.Hits) != 1 || len(res.Warnings) != 0 {
		t.Errorf("res = %+v", res)
	}
	if _, err := f.ix.Backfill(context.Background(), 1, nil); !errors.Is(err, embed.ErrUnavailable) {
		t.Errorf("backfill without embedder: %v", err)
	}
}
