package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/store"
)

func assertPullRequestColumns(t *testing.T, s *Store) {
	t.Helper()
	cols := tableCols(t, s.db, "milestones")
	want := map[string]string{
		"pr_provider":   "TEXT notnull=1 default='' pk=0",
		"pr_namespace":  "TEXT notnull=1 default='' pk=0",
		"pr_repository": "TEXT notnull=1 default='' pk=0",
		"pr_number":     "INTEGER notnull=1 default=0 pk=0",
	}
	for name, w := range want {
		if cols[name] != w {
			t.Errorf("%s = %q, want %q", name, cols[name], w)
		}
	}
}

func TestPullRequestMigrationFreshAndUpgraded(t *testing.T) {
	ctx := context.Background()
	assertPullRequestColumns(t, openTest(t))

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
		if m.name == "0009_milestone_pull_request.sql" {
			break
		}
		if _, err := raw.Exec(m.sql); err != nil {
			t.Fatalf("applying %s: %v", m.name, err)
		}
		if _, err := raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, '2026-01-01T00:00:00Z')`, m.version); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	assertPullRequestColumns(t, s)
}

func TestMilestonePullRequestRecord(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	i, m := seed(t, s)
	if !m.PullRequest.IsZero() {
		t.Errorf("new milestone pull request = %+v", m.PullRequest)
	}
	got, err := s.GetMilestone(ctx, m.ID)
	if err != nil || !got.PullRequest.IsZero() {
		t.Fatalf("GetMilestone = %+v, %v", got.PullRequest, err)
	}

	g := domain.MilestoneGit{Branch: "ralph/m1", BaseCommit: "a", HeadCommit: "b"}
	if err := s.SetMilestoneGit(ctx, m.ID, g); err != nil {
		t.Fatal(err)
	}
	later := m.UpdatedAt.Add(time.Hour)
	s.now = func() time.Time { return later }

	rec := domain.PullRequestRecord{Provider: "github", Namespace: "acme", Repository: "widgets", Number: 42}
	if err := s.SetMilestonePullRequest(ctx, m.ID, rec); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetMilestone(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PullRequest != rec {
		t.Errorf("GetMilestone pull request = %+v, want %+v", got.PullRequest, rec)
	}
	if got.Git != g {
		t.Errorf("git record = %+v, want %+v", got.Git, g)
	}
	if !got.UpdatedAt.Equal(m.UpdatedAt) {
		t.Errorf("updated_at changed: %v -> %v", m.UpdatedAt, got.UpdatedAt)
	}
	ms, err := s.ListMilestones(ctx, i.ID, false)
	if err != nil || len(ms) != 1 || ms[0].PullRequest != rec {
		t.Errorf("ListMilestones = %+v, %v", ms, err)
	}

	got.Title = "renamed"
	if err := s.UpdateMilestone(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.GetMilestone(ctx, m.ID); again.PullRequest != rec || again.Title != "renamed" {
		t.Errorf("after UpdateMilestone = %+v", again)
	}

	if err := s.SetMilestonePullRequest(ctx, m.ID, domain.PullRequestRecord{}); err != nil {
		t.Fatal(err)
	}
	if cleared, _ := s.GetMilestone(ctx, m.ID); !cleared.PullRequest.IsZero() {
		t.Errorf("cleared = %+v", cleared.PullRequest)
	}
	if err := s.SetMilestonePullRequest(ctx, 999, rec); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetMilestonePullRequest(999) = %v, want ErrNotFound", err)
	}

	if err := s.SetMilestonePullRequest(ctx, m.ID, rec); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMilestone(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetMilestone(ctx, m.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetMilestone after delete = %v", err)
	}
}
