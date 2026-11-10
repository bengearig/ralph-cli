package loop

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/search"
	"github.com/bengearig/ralph/internal/service"
	"github.com/bengearig/ralph/internal/store"
	"github.com/bengearig/ralph/internal/store/sqlite"
	"github.com/bengearig/ralph/internal/taskmanager"
)

type countingStore struct {
	store.Store
	gets, writes int
	err          error
}

func (c *countingStore) GetCard(ctx context.Context, kind domain.Kind, id int64, provider string) (domain.Card, error) {
	c.gets++
	if c.err != nil {
		return domain.Card{}, c.err
	}
	return c.Store.GetCard(ctx, kind, id, provider)
}

func (c *countingStore) SetCard(ctx context.Context, card *domain.Card) error {
	c.writes++
	return c.Store.SetCard(ctx, card)
}

type countingProvider struct{ calls int }

func (p *countingProvider) Name() string    { return "jira" }
func (p *countingProvider) Validate() error { return nil }
func (p *countingProvider) Create(context.Context, taskmanager.CardRequest) (taskmanager.Card, error) {
	p.calls++
	return taskmanager.Card{Key: "X-1"}, nil
}
func (p *countingProvider) Update(context.Context, string, taskmanager.CardRequest) error {
	p.calls++
	return nil
}

func cardKeysEnv(t *testing.T) (*service.Service, *countingStore, *countingProvider, int64, int64) {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cs := &countingStore{Store: st}
	prov := &countingProvider{}
	svc := service.New(cs, &search.Index{Store: cs, Chunker: search.Chunker{MaxChars: 500}}, service.Options{StallThreshold: 3})
	i, err := svc.CreateInitiative(ctx, service.InitiativeInput{Title: "SSO"})
	if err != nil {
		t.Fatal(err)
	}
	iID, _ := domain.ParseID(domain.KindInitiative, i.ID)
	m, err := svc.CreateMilestone(ctx, service.MilestoneInput{InitiativeID: iID, Title: "OIDC"})
	if err != nil {
		t.Fatal(err)
	}
	mID, _ := domain.ParseID(domain.KindMilestone, m.ID)
	cs.gets, cs.writes = 0, 0
	svc = service.New(cs, &search.Index{Store: cs, Chunker: search.Chunker{MaxChars: 500}}, service.Options{StallThreshold: 3, TaskManager: prov})
	return svc, cs, prov, iID, mID
}

func cfgFor(t *testing.T, provider string) *config.Config {
	t.Helper()
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	cfg.TaskManager.Provider = provider
	return cfg
}

func TestCardKeysNoneTouchesNothing(t *testing.T) {
	svc, cs, _, iID, mID := cardKeysEnv(t)
	if err := svc.RecordCard(context.Background(), domain.KindMilestone, mID, "jira", "ABC-2", "", ""); err != nil {
		t.Fatal(err)
	}
	cs.gets, cs.writes = 0, 0
	if got := CardKeys(context.Background(), svc, cfgFor(t, config.TaskManagerNone), iID, mID); got != (BranchKeys{}) {
		t.Errorf("keys = %+v", got)
	}
	if cs.gets != 0 || cs.writes != 0 {
		t.Errorf("store touched: gets=%d writes=%d", cs.gets, cs.writes)
	}
}

func TestCardKeysJira(t *testing.T) {
	ctx := context.Background()
	svc, cs, prov, iID, mID := cardKeysEnv(t)
	cfg := cfgFor(t, config.TaskManagerJira)

	if got := CardKeys(ctx, svc, cfg, iID, mID); got != (BranchKeys{}) {
		t.Errorf("no rows: keys = %+v", got)
	}
	if cs.writes != 0 {
		t.Errorf("writes = %d, want 0", cs.writes)
	}

	if err := svc.RecordCard(ctx, domain.KindInitiative, iID, "jira", "ABC-1", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordCard(ctx, domain.KindMilestone, mID, "jira", "ABC-1234", "", ""); err != nil {
		t.Fatal(err)
	}
	cs.writes = 0
	if got := CardKeys(ctx, svc, cfg, iID, mID); got != (BranchKeys{Initiative: "ABC-1", Milestone: "ABC-1234"}) {
		t.Errorf("keys = %+v", got)
	}

	if err := svc.RecordCard(ctx, domain.KindMilestone, mID, "jira", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if got := CardKeys(ctx, svc, cfg, iID, mID); got != (BranchKeys{Initiative: "ABC-1"}) {
		t.Errorf("empty key row: keys = %+v", got)
	}
	if cs.writes != 1 || prov.calls != 0 {
		t.Errorf("writes = %d (want only the test's own), provider calls = %d", cs.writes, prov.calls)
	}
}

func TestCardKeysIgnoresOtherProvider(t *testing.T) {
	ctx := context.Background()
	svc, _, _, iID, mID := cardKeysEnv(t)
	if err := svc.RecordCard(ctx, domain.KindMilestone, mID, "other", "ZZ-9", "", ""); err != nil {
		t.Fatal(err)
	}
	if got := CardKeys(ctx, svc, cfgFor(t, config.TaskManagerJira), iID, mID); got != (BranchKeys{}) {
		t.Errorf("keys = %+v", got)
	}
}

func TestCardKeysSwallowsStoreErrors(t *testing.T) {
	svc, cs, _, iID, mID := cardKeysEnv(t)
	cs.err = errors.New("boom")
	if got := CardKeys(context.Background(), svc, cfgFor(t, config.TaskManagerJira), iID, mID); got != (BranchKeys{}) {
		t.Errorf("keys = %+v", got)
	}
}

func resolveEnv(t *testing.T, tmpl string) (*service.Service, *config.Config, service.InitiativeView, service.MilestoneView) {
	t.Helper()
	svc, _, _, iID, mID := cardKeysEnv(t)
	cfg := cfgFor(t, config.TaskManagerJira)
	cfg.Loop.BranchTemplate = tmpl
	i, err := svc.GetInitiative(context.Background(), iID)
	if err != nil {
		t.Fatal(err)
	}
	m, err := svc.GetMilestone(context.Background(), mID)
	if err != nil {
		t.Fatal(err)
	}
	return svc, cfg, i, m
}

func TestResolveBranch(t *testing.T) {
	ctx := context.Background()
	const keyed = "{{.Milestone.CardKeyPrefix}}{{.Milestone.Slug}}"
	svc, cfg, i, m := resolveEnv(t, keyed)
	mID, _ := domain.ParseID(domain.KindMilestone, m.ID)

	if got, err := ResolveBranch(ctx, svc, cfg, i, m); err != nil || got != "oidc" {
		t.Errorf("no card: %q, %v", got, err)
	}
	if err := svc.RecordCard(ctx, domain.KindMilestone, mID, "jira", "ABC-1234", "", ""); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveBranch(ctx, svc, cfg, i, m); err != nil || got != "ABC-1234-oidc" {
		t.Errorf("card: %q, %v", got, err)
	}

	cfg.Loop.BranchTemplate = "ralph/{{.Milestone.Slug}}"
	if got, err := ResolveBranch(ctx, svc, cfg, i, m); err != nil || got != "ralph/oidc" {
		t.Errorf("default template: %q, %v", got, err)
	}

	cfg.Loop.BranchTemplate = keyed
	m.Branch = "feature/custom"
	if got, err := ResolveBranch(ctx, svc, cfg, i, m); err != nil || got != "feature/custom" {
		t.Errorf("recorded: %q, %v", got, err)
	}
}
