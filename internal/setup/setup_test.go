package setup

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/harness"

	_ "github.com/bengearig/ralph/internal/harness/claudecode"
)

func defaults(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func run(t *testing.T, o Options) []harness.Change {
	t.Helper()
	if o.Config == nil {
		o.Config = defaults(t)
	}
	changes, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return changes
}

type entry struct {
	Mode    fs.FileMode
	Content string
	ModTime time.Time
}

func snapshot(t *testing.T, root string) map[string]entry {
	t.Helper()
	snap := map[string]entry{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		e := entry{Mode: info.Mode(), ModTime: info.ModTime()}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			e.Content = string(data)
		}
		snap[rel] = e
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func copyDir(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDryRunMatchesRealRun(t *testing.T) {
	initialized := func(t *testing.T) string {
		root := t.TempDir()
		run(t, Options{Root: root})
		return root
	}
	cases := []struct {
		name  string
		setup func(t *testing.T) string
		opts  Options
		empty bool
	}{
		{"empty directory", func(t *testing.T) string { return t.TempDir() }, Options{}, true},
		{"initialized", initialized, Options{}, false},
		{"missing gitignore pattern", func(t *testing.T) string {
			root := initialized(t)
			write(t, root, ".gitignore", "/.ralph/ralph.db*\n")
			return root
		}, Options{}, false},
		{"stale command file", func(t *testing.T) string {
			root := initialized(t)
			if err := os.Remove(filepath.Join(root, ".claude/commands/ralph/import.md")); err != nil {
				t.Fatal(err)
			}
			write(t, root, ".claude/commands/ralph/receive.md", "customized\n")
			return root
		}, Options{}, false},
		{"force", initialized, Options{Force: true}, false},
		{"force on empty directory", func(t *testing.T) string { return t.TempDir() }, Options{Force: true}, true},
		{"no harness", initialized, Options{NoHarness: true}, false},
		{"no harness on empty directory", func(t *testing.T) string { return t.TempDir() }, Options{NoHarness: true}, true},
		{"custom ralph command", func(t *testing.T) string { return t.TempDir() }, Options{RalphCommand: "/bin/ralph", KeepMCPCommand: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dry := tc.setup(t)
			real := copyDir(t, dry)

			before := snapshot(t, dry)
			do := tc.opts
			do.Root, do.DryRun = dry, true
			dryChanges := run(t, do)
			if after := snapshot(t, dry); !reflect.DeepEqual(before, after) {
				t.Errorf("dry run modified the directory:\nbefore %v\nafter  %v", before, after)
			}
			if tc.empty && len(snapshot(t, dry)) != 1 {
				t.Errorf("dry run created files in an empty directory: %v", snapshot(t, dry))
			}

			ro := tc.opts
			ro.Root = real
			realChanges := run(t, ro)
			if !reflect.DeepEqual(dryChanges, realChanges) {
				t.Errorf("dry run changes differ:\ndry  %v\nreal %v", dryChanges, realChanges)
			}
		})
	}
}

func TestDryRunNeverOpensDatabase(t *testing.T) {
	root := t.TempDir()
	changes := run(t, Options{Root: root, DryRun: true, NoHarness: true})
	if changes[1].Path != filepath.Join(".ralph", "ralph.db") || changes[1].Action != harness.Created {
		t.Errorf("database change = %+v", changes[1])
	}
	if _, err := os.Stat(filepath.Join(root, ".ralph", "ralph.db")); !os.IsNotExist(err) {
		t.Errorf("database exists after dry run: %v", err)
	}

	db := filepath.Join(root, ".ralph", "ralph.db")
	write(t, root, ".ralph/ralph.db", "not a database")
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(db, old, old); err != nil {
		t.Fatal(err)
	}
	changes = run(t, Options{Root: root, DryRun: true, NoHarness: true})
	if changes[1].Action != harness.Unchanged {
		t.Errorf("database change = %+v", changes[1])
	}
	info, err := os.Stat(db)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(db)
	if !info.ModTime().Equal(old) || string(data) != "not a database" {
		t.Errorf("database touched: mtime %v, content %q", info.ModTime(), data)
	}
	entries, _ := os.ReadDir(filepath.Join(root, ".ralph"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "ralph.db-") {
			t.Errorf("unexpected %s", e.Name())
		}
	}
}

func TestIgnoreGitignore(t *testing.T) {
	for _, dry := range []bool{true, false} {
		root := t.TempDir()
		write(t, root, ".gitignore", "node_modules\n")
		changes := run(t, Options{Root: root, DryRun: dry, NoHarness: true, Ignore: []string{".gitignore"}})
		want := harness.Change{Path: ".gitignore", Action: harness.Unchanged, Note: "ignored by init.ignore"}
		if changes[2] != want {
			t.Errorf("dry=%v: change = %+v", dry, changes[2])
		}
		data, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
		if string(data) != "node_modules\n" {
			t.Errorf("dry=%v: .gitignore modified: %q", dry, data)
		}
	}
}

func TestLoadConfigAfterTemplate(t *testing.T) {
	root := t.TempDir()
	var sawTemplate bool
	_, err := Run(context.Background(), Options{Root: root, NoHarness: true, LoadConfig: func() (*config.Config, error) {
		_, err := os.Stat(filepath.Join(root, ".ralph", "config.yaml"))
		sawTemplate = err == nil
		return defaults(t), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !sawTemplate {
		t.Error("config loaded before the template was written")
	}
}
