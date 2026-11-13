package setup

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/harness"
)

func installed(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	run(t, Options{Root: root})
	if err := os.Remove(filepath.Join(root, ".ralph", "ralph.db")); err != nil {
		t.Fatal(err)
	}
	return root
}

func check(t *testing.T, root string, cfg *config.Config) CheckResult {
	t.Helper()
	res, err := Check(context.Background(), root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func paths(cs []harness.Change) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Path+" "+c.Action)
	}
	return out
}

const importPath = ".claude/commands/ralph/import.md"

func TestCheckNotInstalled(t *testing.T) {
	for name, runOpts := range map[string]*Options{
		"config only": nil,
		"no harness":  {NoHarness: true},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if runOpts == nil {
				write(t, root, ".ralph/config.yaml", "")
			} else {
				runOpts.Root = root
				run(t, *runOpts)
			}
			before := snapshot(t, root)
			res := check(t, root, defaults(t))
			if res.Installed || res.Stale != nil || res.Customized != nil {
				t.Errorf("result = %+v", res)
			}
			if !reflect.DeepEqual(before, snapshot(t, root)) {
				t.Error("files changed")
			}
		})
	}
}

func TestCheckCurrent(t *testing.T) {
	root := installed(t)
	res := check(t, root, defaults(t))
	if !res.Installed || len(res.Stale) != 0 || len(res.Customized) != 0 {
		t.Errorf("result = %+v", res)
	}
}

func TestCheckInstalledSignals(t *testing.T) {
	cases := map[string]func(t *testing.T, root string){
		"mcp entry": func(t *testing.T, root string) {
			write(t, root, ".mcp.json", `{"mcpServers":{"ralph":{"type":"stdio","command":"ralph","args":["mcp"]}}}`)
		},
		"commands dir": func(t *testing.T, root string) {
			if err := os.MkdirAll(filepath.Join(root, ".claude/commands/ralph"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"claude.md block": func(t *testing.T, root string) {
			write(t, root, "CLAUDE.md", "# x\n\n<!-- ralph:begin -->\nold\n<!-- ralph:end -->\n")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			setup(t, root)
			if res := check(t, root, defaults(t)); !res.Installed || len(res.Stale) == 0 {
				t.Errorf("result = %+v", res)
			}
		})
	}
}

func TestCheckStale(t *testing.T) {
	earlier, err := os.ReadFile("testdata/import-earlier.md")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, root string)
		want  string
	}{
		{"earlier shipped command", func(t *testing.T, root string) {
			write(t, root, importPath, string(earlier))
		}, importPath + " updated"},
		{"missing command", func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, importPath)); err != nil {
				t.Fatal(err)
			}
		}, importPath + " created"},
		{"outdated CLAUDE.md block", func(t *testing.T, root string) {
			write(t, root, "CLAUDE.md", "<!-- ralph:begin -->\nold\n<!-- ralph:end -->\n")
		}, "CLAUDE.md updated"},
		{"missing permission", func(t *testing.T, root string) {
			write(t, root, ".claude/settings.json", `{"permissions":{"allow":[]}}`)
		}, ".claude/settings.json updated"},
		{"missing gitignore pattern", func(t *testing.T, root string) {
			write(t, root, ".gitignore", "/.ralph/ralph.db*\n")
		}, ".gitignore updated"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := installed(t)
			tc.setup(t, root)
			res := check(t, root, defaults(t))
			if got := paths(res.Stale); !reflect.DeepEqual(got, []string{tc.want}) {
				t.Errorf("stale = %v, want [%s]", got, tc.want)
			}
			if len(res.Customized) != 0 {
				t.Errorf("customized = %v", res.Customized)
			}
		})
	}
}

func TestCheckCustomized(t *testing.T) {
	root := installed(t)
	write(t, root, ".claude/commands/ralph/receive.md", "mine\n")
	res := check(t, root, defaults(t))
	if got := paths(res.Customized); !reflect.DeepEqual(got, []string{".claude/commands/ralph/receive.md skipped"}) || len(res.Stale) != 0 {
		t.Errorf("result = %+v", res)
	}
}

func TestCheckMCPEntry(t *testing.T) {
	root := installed(t)
	write(t, root, ".mcp.json", `{"mcpServers":{"ralph":{"type":"stdio","command":"/opt/bin/ralph","args":["mcp"]}}}`)
	if res := check(t, root, defaults(t)); len(res.Stale)+len(res.Customized) != 0 {
		t.Errorf("absolute command reported: %+v", res)
	}
	write(t, root, ".mcp.json", `{"mcpServers":{"ralph":{"type":"stdio","command":"ralph","args":["mcp","--x"]}}}`)
	res := check(t, root, defaults(t))
	if got := paths(res.Customized); !reflect.DeepEqual(got, []string{".mcp.json skipped"}) || len(res.Stale) != 0 {
		t.Errorf("result = %+v", res)
	}
}

func TestCheckIgnore(t *testing.T) {
	root := installed(t)
	write(t, root, importPath, "customized\n")
	write(t, root, "CLAUDE.md", "<!-- ralph:begin -->\nold\n<!-- ralph:end -->\n")
	write(t, root, ".gitignore", "")
	if res := check(t, root, defaults(t)); len(res.Stale) != 2 || len(res.Customized) != 1 {
		t.Fatalf("without ignore: %+v", res)
	}
	cfg := defaults(t)
	cfg.Init.Ignore = []string{importPath, "CLAUDE.md", ".gitignore"}
	if res := check(t, root, cfg); !res.Installed || len(res.Stale)+len(res.Customized) != 0 {
		t.Errorf("with ignore: %+v", res)
	}
}

func TestCheckReadOnly(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()

	root := installed(t)
	write(t, root, importPath, "customized\n")
	write(t, root, "CLAUDE.md", "<!-- ralph:begin -->\nold\n<!-- ralph:end -->\n")
	if err := os.Remove(filepath.Join(root, ".claude/commands/ralph/search.md")); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, root)
	if res := check(t, root, defaults(t)); len(res.Stale) == 0 {
		t.Fatalf("expected stale entries: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, ".ralph", "ralph.db")); !os.IsNotExist(err) {
		t.Errorf("database exists: %v", err)
	}
	if !reflect.DeepEqual(before, snapshot(t, root)) {
		t.Error("files changed")
	}
	if hits.Load() != 0 {
		t.Errorf("%d network requests", hits.Load())
	}
}

func TestCheckInvalidJSON(t *testing.T) {
	root := installed(t)
	write(t, root, ".mcp.json", "{not json")
	if _, err := Check(context.Background(), root, defaults(t)); err == nil || !strings.Contains(err.Error(), ".mcp.json") {
		t.Errorf("err = %v", err)
	}
}
