package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/store"
)

func card(kind domain.Kind, id int64, provider, key string) domain.Card {
	return domain.Card{Kind: kind, EntityID: id, Provider: provider, Key: key, URL: "https://x/" + key,
		ContentHash: "h-" + key}
}

func cardCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM cards`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCardsMigrationFreshAndUpgraded(t *testing.T) {
	ctx := context.Background()
	fresh := openTest(t)
	if cardCount(t, fresh) != 0 {
		t.Fatal("fresh cards table not empty")
	}

	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	if _, err := raw.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.name == "0005_cards.sql" {
			break
		}
		if _, err := raw.Exec(m.sql); err != nil {
			t.Fatalf("applying %s: %v", m.name, err)
		}
		if _, err := raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, '2026-01-01T00:00:00Z')`, m.version); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := raw.Exec(`INSERT INTO initiatives (slug, title, brief, created_at, updated_at)
		VALUES ('a', 'A', 'brief', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if cardCount(t, s) != 0 {
		t.Error("upgraded cards table not empty")
	}
	if got, err := s.GetInitiative(ctx, 1); err != nil || got.Title != "A" || got.Brief != "brief" {
		t.Errorf("existing initiative = %+v, %v", got, err)
	}
	c := card(domain.KindInitiative, 1, "jira", "P-1")
	if err := s.SetCard(ctx, &c); err != nil {
		t.Errorf("SetCard on upgraded database: %v", err)
	}
}

func TestCardRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	at := time.Date(2026, 3, 4, 5, 6, 7, 8, time.UTC)
	c := card(domain.KindTask, 7, "jira", "P-7")
	c.LastError, c.AttemptedAt, c.SyncedAt = "boom", at, at.Add(-time.Hour)
	if err := s.SetCard(ctx, &c); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCard(ctx, domain.KindTask, 7, "jira")
	if err != nil || got != c {
		t.Fatalf("GetCard = %+v, %v; want %+v", got, err, c)
	}

	zero := card(domain.KindTask, 8, "jira", "")
	if err := s.SetCard(ctx, &zero); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetCard(ctx, domain.KindTask, 8, "jira"); err != nil || !got.AttemptedAt.IsZero() || !got.SyncedAt.IsZero() {
		t.Errorf("zero times = %+v, %v", got, err)
	}

	c.Key, c.LastError = "P-70", ""
	if err := s.SetCard(ctx, &c); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetCard(ctx, domain.KindTask, 7, "jira"); got.Key != "P-70" || got.LastError != "" {
		t.Errorf("replaced = %+v", got)
	}
	if n := cardCount(t, s); n != 2 {
		t.Errorf("rows after replace = %d, want 2", n)
	}

	other := card(domain.KindTask, 7, "other", "O-1")
	if err := s.SetCard(ctx, &other); err != nil {
		t.Fatal(err)
	}
	if n := cardCount(t, s); n != 3 {
		t.Errorf("rows with second provider = %d, want 3", n)
	}

	if _, err := s.GetCard(ctx, domain.KindTask, 99, "jira"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing GetCard err = %v, want ErrNotFound", err)
	}
	bad := card(domain.KindDocument, 1, "jira", "X")
	if err := s.SetCard(ctx, &bad); err == nil {
		t.Error("SetCard accepted kind document")
	}
}

func TestListAndDeleteCards(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	for _, c := range []domain.Card{
		card(domain.KindTask, 2, "jira", "a"), card(domain.KindMilestone, 9, "jira", "b"),
		card(domain.KindTask, 1, "jira", "c"), card(domain.KindInitiative, 5, "jira", "d"),
		card(domain.KindTask, 1, "other", "e"),
	} {
		if err := s.SetCard(ctx, &c); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListCards(ctx, "jira")
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, c := range got {
		keys = append(keys, c.Key)
	}
	if want := []string{"d", "b", "c", "a"}; len(keys) != 4 || keys[0] != want[0] || keys[1] != want[1] || keys[2] != want[2] || keys[3] != want[3] {
		t.Errorf("ListCards order = %v, want %v", keys, want)
	}

	if err := s.DeleteCards(ctx, domain.KindTask, 1); err != nil {
		t.Fatal(err)
	}
	if n := cardCount(t, s); n != 3 {
		t.Errorf("rows after DeleteCards = %d, want 3", n)
	}
	if err := s.DeleteCards(ctx, domain.KindTask, 1); err != nil {
		t.Errorf("repeat DeleteCards: %v", err)
	}
}

func cardExists(s *Store, kind domain.Kind, id int64) bool {
	_, err := s.GetCard(context.Background(), kind, id, "jira")
	return err == nil
}

func TestDeleteCascadesToCards(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	i, m1 := seed(t, s)
	m2 := domain.Milestone{InitiativeID: i.ID, Slug: "m2", Title: "M2"}
	if err := s.CreateMilestone(ctx, &m2); err != nil {
		t.Fatal(err)
	}
	i2 := domain.Initiative{Slug: "other", Title: "Other", Brief: "b"}
	if err := s.CreateInitiative(ctx, &i2); err != nil {
		t.Fatal(err)
	}
	m3 := domain.Milestone{InitiativeID: i2.ID, Slug: "m3", Title: "M3"}
	if err := s.CreateMilestone(ctx, &m3); err != nil {
		t.Fatal(err)
	}
	mk := func(m domain.Milestone, title string) domain.Task {
		task := domain.Task{MilestoneID: m.ID, Title: title, Status: domain.StatusProposed}
		if err := s.CreateTask(ctx, &task); err != nil {
			t.Fatal(err)
		}
		return task
	}
	t1, t2, t3, t4 := mk(m1, "t1"), mk(m1, "t2"), mk(m2, "t3"), mk(m3, "t4")
	for _, e := range []struct {
		k  domain.Kind
		id int64
	}{
		{domain.KindInitiative, i.ID}, {domain.KindInitiative, i2.ID},
		{domain.KindMilestone, m1.ID}, {domain.KindMilestone, m2.ID}, {domain.KindMilestone, m3.ID},
		{domain.KindTask, t1.ID}, {domain.KindTask, t2.ID}, {domain.KindTask, t3.ID}, {domain.KindTask, t4.ID},
	} {
		c := card(e.k, e.id, "jira", "K")
		if err := s.SetCard(ctx, &c); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.DeleteTask(ctx, t1.ID); err != nil {
		t.Fatal(err)
	}
	if cardExists(s, domain.KindTask, t1.ID) || !cardExists(s, domain.KindTask, t2.ID) || cardCount(t, s) != 8 {
		t.Errorf("after task delete: %d rows", cardCount(t, s))
	}

	rollback := errors.New("rollback")
	err := s.InTx(ctx, func(tx store.Store) error {
		if err := tx.DeleteMilestone(ctx, m1.ID); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) || cardCount(t, s) != 8 {
		t.Errorf("rolled back delete left %d rows (err %v), want 8", cardCount(t, s), err)
	}

	if err := s.DeleteMilestone(ctx, m1.ID); err != nil {
		t.Fatal(err)
	}
	if cardExists(s, domain.KindMilestone, m1.ID) || cardExists(s, domain.KindTask, t2.ID) ||
		!cardExists(s, domain.KindMilestone, m2.ID) || !cardExists(s, domain.KindTask, t3.ID) || cardCount(t, s) != 6 {
		t.Errorf("after milestone delete: %d rows", cardCount(t, s))
	}

	if err := s.DeleteInitiative(ctx, i.ID); err != nil {
		t.Fatal(err)
	}
	if n := cardCount(t, s); n != 3 {
		t.Errorf("after initiative delete: %d rows, want 3", n)
	}
	for _, e := range []struct {
		k  domain.Kind
		id int64
	}{{domain.KindInitiative, i2.ID}, {domain.KindMilestone, m3.ID}, {domain.KindTask, t4.ID}} {
		if !cardExists(s, e.k, e.id) {
			t.Errorf("%s card of the other initiative was removed", domain.FormatID(e.k, e.id))
		}
	}

	if err := s.DeleteTask(ctx, 9999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("deleting missing task err = %v, want ErrNotFound", err)
	}
}

func TestClaimCard(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	claim := func(provider string, stale time.Duration) bool {
		t.Helper()
		ok, err := s.ClaimCard(ctx, domain.KindTask, 1, provider, stale)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	if !claim("jira", time.Minute) {
		t.Fatal("first claim refused")
	}
	c, err := s.GetCard(ctx, domain.KindTask, 1, "jira")
	if err != nil || c.Key != "" || c.AttemptedAt.IsZero() {
		t.Fatalf("row = %+v, %v", c, err)
	}
	if claim("jira", time.Minute) {
		t.Fatal("second claim accepted")
	}
	again, _ := s.GetCard(ctx, domain.KindTask, 1, "jira")
	if !again.AttemptedAt.Equal(c.AttemptedAt) {
		t.Error("refused claim changed the row")
	}

	old := c
	old.AttemptedAt = time.Now().Add(-time.Hour)
	if err := s.SetCard(ctx, &old); err != nil {
		t.Fatal(err)
	}
	if !claim("jira", time.Minute) {
		t.Fatal("stale claim refused")
	}
	taken, _ := s.GetCard(ctx, domain.KindTask, 1, "jira")
	if !taken.AttemptedAt.After(old.AttemptedAt.Add(30 * time.Minute)) {
		t.Errorf("attempted_at not refreshed: %v", taken.AttemptedAt)
	}

	keyed := card(domain.KindTask, 1, "jira", "K-1")
	keyed.AttemptedAt = time.Now().Add(-time.Hour)
	if err := s.SetCard(ctx, &keyed); err != nil {
		t.Fatal(err)
	}
	if claim("jira", time.Minute) {
		t.Fatal("claimed a row that has a key")
	}
	if got, _ := s.GetCard(ctx, domain.KindTask, 1, "jira"); got.Key != "K-1" {
		t.Errorf("key = %q", got.Key)
	}

	if !claim("other", time.Minute) {
		t.Error("providers do not claim independently")
	}
}

func TestClaimCardRace(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "race.db")
	var stores []*Store
	for i := 0; i < 4; i++ {
		st, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		stores = append(stores, st)
	}
	const n = 20
	var wg sync.WaitGroup
	var won atomic.Int32
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := stores[i%len(stores)].ClaimCard(ctx, domain.KindTask, 7, "jira", time.Minute)
			if err != nil {
				t.Error(err)
			}
			if ok {
				won.Add(1)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Errorf("winners = %d, want 1", won.Load())
	}
}

func TestCardParentKey(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	c := card(domain.KindInitiative, 1, "jira", "P-1")
	if err := s.SetCard(ctx, &c); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetCard(ctx, c.Kind, 1, "jira"); err != nil || got.ParentKey != "" {
		t.Fatalf("new card = %+v, %v", got, err)
	}
	if err := s.SetCardParentKey(ctx, c.Kind, 1, "jira", "PROJ-12"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetCard(ctx, c.Kind, 1, "jira"); got.ParentKey != "PROJ-12" || got.Key != "P-1" {
		t.Errorf("GetCard = %+v", got)
	}
	if all, err := s.ListCards(ctx, "jira"); err != nil || len(all) != 1 || all[0].ParentKey != "PROJ-12" {
		t.Errorf("ListCards = %+v, %v", all, err)
	}

	c.ContentHash = "later"
	if err := s.SetCard(ctx, &c); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetCard(ctx, c.Kind, 1, "jira"); got.ParentKey != "PROJ-12" || got.ContentHash != "later" {
		t.Errorf("after SetCard = %+v", got)
	}
	if _, err := s.ClaimCard(ctx, c.Kind, 1, "jira", time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetCard(ctx, c.Kind, 1, "jira"); got.ParentKey != "PROJ-12" {
		t.Errorf("after ClaimCard = %+v", got)
	}

	if err := s.SetCardParentKey(ctx, c.Kind, 1, "jira", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetCard(ctx, c.Kind, 1, "jira"); got.ParentKey != "" {
		t.Errorf("cleared = %+v", got)
	}
	if err := s.SetCardParentKey(ctx, c.Kind, 2, "jira", "X-1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing card err = %v", err)
	}
}

func TestCardParentKeyMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	if _, err := raw.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.name == "0011_card_parent_key.sql" {
			break
		}
		if _, err := raw.Exec(m.sql); err != nil {
			t.Fatalf("applying %s: %v", m.name, err)
		}
		if _, err := raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, '2026-01-01T00:00:00Z')`, m.version); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := raw.Exec(`INSERT INTO initiatives (slug, title, brief, created_at, updated_at)
		VALUES ('a', 'A', 'brief', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO cards (kind, entity_id, provider, card_key) VALUES ('initiative', 1, 'jira', 'P-1')`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	got, err := s.GetCard(ctx, domain.KindInitiative, 1, "jira")
	if err != nil || got.Key != "P-1" || got.ParentKey != "" {
		t.Errorf("existing card = %+v, %v", got, err)
	}
	var def string
	if err := s.db.QueryRow(`SELECT dflt_value FROM pragma_table_info('cards') WHERE name = 'parent_key'`).Scan(&def); err != nil || def != "''" {
		t.Errorf("parent_key default = %q, %v", def, err)
	}
	fresh := openTest(t)
	if err := fresh.db.QueryRow(`SELECT dflt_value FROM pragma_table_info('cards') WHERE name = 'parent_key'`).Scan(&def); err != nil || def != "''" {
		t.Errorf("fresh parent_key default = %q, %v", def, err)
	}
}
