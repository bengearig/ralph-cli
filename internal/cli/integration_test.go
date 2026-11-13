package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/harness"
	"github.com/bengearig/ralph/internal/looplock"
	"github.com/bengearig/ralph/internal/project"
	"github.com/bengearig/ralph/internal/setup"
)

const (
	staleCmd      = ".claude/commands/ralph/import.md"
	outOfDateWarn = "the integration files in this repository are out of date: "
	customWarn    = "these customized files are left alone: "
)

func staleEnv(t *testing.T) *testEnv {
	t.Helper()
	h := newTestEnv(t, true)
	t.Setenv("RALPH_INIT__UPDATE", "manual")
	h.installHarness()
	earlier, err := os.ReadFile("../setup/testdata/import-earlier.md")
	if err != nil {
		t.Fatal(err)
	}
	h.write(staleCmd, string(earlier))
	return h
}

func TestIntegrationUpToDateNoWarning(t *testing.T) {
	h := newTestEnv(t, true)
	h.installHarness()
	_, errOut, err := h.run("", "status")
	if err != nil || strings.Contains(errOut, "integration") {
		t.Errorf("err=%v stderr=%q", err, errOut)
	}
}

func TestIntegrationStaleWarns(t *testing.T) {
	h := staleEnv(t)
	plain, _, err := h.run("", "status")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		out, errOut, err := h.run("", "status")
		if err != nil {
			t.Fatal(err)
		}
		if out != plain {
			t.Errorf("stdout changed: %q vs %q", out, plain)
		}
		if strings.Count(errOut, outOfDateWarn+staleCmd) != 1 || !strings.Contains(errOut, "run `ralph init` to update them") {
			t.Errorf("stderr = %q", errOut)
		}
		if strings.Contains(errOut, customWarn) {
			t.Errorf("unexpected customized warning: %q", errOut)
		}
	}

	jsonPlain, _, _ := h.run("", "--json", "status")
	out, errOut, err := h.run("", "--json", "status")
	if err != nil || !json.Valid([]byte(out)) || out != jsonPlain {
		t.Errorf("json out=%q err=%v", out, err)
	}
	if !strings.Contains(errOut, outOfDateWarn+staleCmd) {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestIntegrationCustomizedAndIgnore(t *testing.T) {
	h := newTestEnv(t, true)
	h.installHarness()
	const path = ".claude/commands/ralph/receive.md"
	h.write(path, "my own receive\n")

	_, errOut, err := h.run("", "status")
	if err != nil || !strings.Contains(errOut, customWarn+path) || !strings.Contains(errOut, "init.ignore") {
		t.Errorf("err=%v stderr=%q", err, errOut)
	}
	if strings.Contains(errOut, outOfDateWarn) {
		t.Errorf("unexpected stale warning: %q", errOut)
	}

	h.write(".ralph/config.yaml", "init:\n  ignore:\n    - "+path+"\n")
	if _, errOut, _ := h.run("", "status"); strings.Contains(errOut, "integration") || strings.Contains(errOut, customWarn) {
		t.Errorf("ignored file still warns: %q", errOut)
	}
}

func TestIntegrationAbsoluteMCPCommand(t *testing.T) {
	h := newTestEnv(t, true)
	h.installHarness()
	h.write(".mcp.json", `{"mcpServers":{"ralph":{"type":"stdio","command":"/opt/bin/ralph","args":["mcp"]}}}`)
	_, errOut, err := h.run("", "status")
	if err != nil || strings.Contains(errOut, "integration") {
		t.Errorf("err=%v stderr=%q", err, errOut)
	}
}

func TestIntegrationNotInstalledOrOutsideProject(t *testing.T) {
	h := newTestEnv(t, true)
	if _, errOut, err := h.run("", "status"); err != nil || strings.Contains(errOut, "integration") {
		t.Errorf("err=%v stderr=%q", err, errOut)
	}

	outside := newTestEnv(t, false)
	t.Chdir(outside.dir)
	var out, errOut strings.Builder
	err := Execute(context.Background(), Streams{In: strings.NewReader(""), Out: &out, Err: &errOut}, []string{"version"})
	if err != nil || strings.Contains(errOut.String(), "integration") {
		t.Errorf("err=%v stderr=%q", err, errOut.String())
	}
	errOut.Reset()
	_ = Execute(context.Background(), Streams{In: strings.NewReader(""), Out: &out, Err: &errOut}, []string{"config", "show"})
	if strings.Contains(errOut.String(), "integration") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestIntegrationExemptCommands(t *testing.T) {
	h := staleEnv(t)
	for _, args := range [][]string{{"version"}, {"completion", "bash"}, {"help"}, {"init", "--dry-run"}} {
		_, errOut, err := h.run("", args...)
		if err != nil || strings.Contains(errOut, "integration files") {
			t.Errorf("%v: err=%v stderr=%q", args, err, errOut)
		}
	}
}

func TestIntegrationManualModeOnlyWarns(t *testing.T) {
	h := staleEnv(t)
	t.Setenv("RALPH_INIT__UPDATE", "manual")
	before := h.snapshot()
	_, errOut, err := h.run("", "status")
	if err != nil || !strings.Contains(errOut, outOfDateWarn+staleCmd) {
		t.Errorf("err=%v stderr=%q", err, errOut)
	}
	if !reflect.DeepEqual(before, h.snapshot()) {
		t.Error("files changed")
	}
}

const (
	updatedWarn = "updated its integration files: "
	modifiedCmd = ".claude/commands/ralph/receive.md"
)

func autoEnv(t *testing.T) *testEnv {
	t.Helper()
	h := staleEnv(t)
	t.Setenv("RALPH_INIT__UPDATE", "auto")
	return h
}

func TestIntegrationAutoUpdates(t *testing.T) {
	h := autoEnv(t)
	h.write(modifiedCmd, "my own receive\n")
	if err := os.Remove(filepath.Join(h.dir, filepath.FromSlash(".claude/commands/ralph/search.md"))); err != nil {
		t.Fatal(err)
	}
	mcp := `{"mcpServers":{"ralph":{"type":"stdio","command":"/opt/bin/ralph","args":["mcp"]}}}`
	h.write(".mcp.json", mcp)
	plain := func() string {
		h2 := newTestEnv(t, true)
		h2.installHarness()
		o, _, _ := h2.run("", "--json", "status")
		return o
	}()

	out, errOut, err := h.run("", "status")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(errOut, updatedWarn) != 1 || !strings.Contains(errOut, staleCmd+" (updated)") ||
		!strings.Contains(errOut, ".claude/commands/ralph/search.md (created)") ||
		!strings.Contains(errOut, "review and commit them") || strings.Contains(errOut, outOfDateWarn) ||
		!strings.Contains(errOut, customWarn+modifiedCmd) {
		t.Errorf("stderr = %q", errOut)
	}
	if strings.Contains(out, "integration") {
		t.Errorf("stdout = %q", out)
	}
	want, err := os.ReadFile("../setup/testdata/import-earlier.md")
	if err != nil {
		t.Fatal(err)
	}
	if got := h.read(staleCmd); got == string(want) {
		t.Error("stale command was not rewritten")
	}
	if h.read(".mcp.json") != mcp || h.read(modifiedCmd) != "my own receive\n" {
		t.Error("mcp command or modified command changed")
	}

	before := h.snapshot()
	_, errOut, err = h.run("", "status")
	if err != nil || strings.Contains(errOut, updatedWarn) || strings.Contains(errOut, outOfDateWarn) {
		t.Errorf("second run: err=%v stderr=%q", err, errOut)
	}
	if !reflect.DeepEqual(before, h.snapshot()) {
		t.Error("second run wrote files")
	}
	if out2, _, _ := h.run("", "--json", "status"); !json.Valid([]byte(out2)) || out2 != plain {
		t.Errorf("json = %q", out2)
	}
}

func TestIntegrationAutoIgnoreNotUpdated(t *testing.T) {
	h := autoEnv(t)
	h.write(".ralph/config.yaml", "init:\n  ignore:\n    - "+staleCmd+"\n")
	before := h.read(staleCmd)
	_, errOut, err := h.run("", "status")
	if err != nil || strings.Contains(errOut, "integration") || h.read(staleCmd) != before {
		t.Errorf("err=%v stderr=%q", err, errOut)
	}
}

func TestIntegrationAutoSkippedForDryRunAndLoopLock(t *testing.T) {
	h := autoEnv(t)
	before := h.snapshot()
	_, errOut, _ := h.run("", "pr", "create", "--milestone", "M-1", "--dry-run")
	if !strings.Contains(errOut, outOfDateWarn+staleCmd) || strings.Contains(errOut, updatedWarn) {
		t.Errorf("dry run stderr = %q", errOut)
	}
	if !reflect.DeepEqual(before, h.snapshot()) {
		t.Error("dry run changed files")
	}

	lock, err := looplock.Acquire(filepath.Join(h.dir, project.DirName, "loop.lock"), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	before = h.snapshot()
	_, errOut, err = h.run("", "status")
	if err != nil || !strings.Contains(errOut, outOfDateWarn+staleCmd) || strings.Contains(errOut, updatedWarn) {
		t.Errorf("err=%v stderr=%q", err, errOut)
	}
	if !reflect.DeepEqual(before, h.snapshot()) {
		t.Error("held lock changed files")
	}
}

func TestIntegrationAutoWriteFailureWarns(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions are not enforced for root")
	}
	h := autoEnv(t)
	if err := os.Chmod(filepath.Join(h.dir, filepath.FromSlash(staleCmd)), 0o444); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := h.run("", "--json", "status")
	if err != nil || !json.Valid([]byte(out)) || !strings.Contains(errOut, "could not update ralph's integration files:") {
		t.Errorf("err=%v stdout=%q stderr=%q", err, out, errOut)
	}
}

func TestIntegrationAutoNeverUpdatesWithoutHarness(t *testing.T) {
	h := newTestEnv(t, true)
	h.ok("init", "--no-harness")
	_, errOut, err := h.run("", "status")
	if err != nil || strings.Contains(errOut, "integration") {
		t.Errorf("err=%v stderr=%q", err, errOut)
	}
	for _, p := range []string{".mcp.json", "CLAUDE.md", ".claude"} {
		if _, err := os.Stat(filepath.Join(h.dir, p)); err == nil {
			t.Errorf("%s was created", p)
		}
	}
}

func TestIntegrationAutoDoesNotOpenDatabase(t *testing.T) {
	h := autoEnv(t)
	matches, _ := filepath.Glob(filepath.Join(h.dir, project.DirName, "ralph.db*"))
	for _, m := range matches {
		_ = os.Remove(m)
	}
	_, errOut, err := h.run("", "config", "show")
	if err != nil || !strings.Contains(errOut, updatedWarn) {
		t.Fatalf("err=%v stderr=%q", err, errOut)
	}
	if m, _ := filepath.Glob(filepath.Join(h.dir, project.DirName, "ralph.db*")); len(m) > 0 {
		t.Errorf("database appeared: %v", m)
	}
}

func (h *testEnv) read(rel string) string {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.dir, filepath.FromSlash(rel)))
	if err != nil {
		h.t.Fatal(err)
	}
	return string(data)
}

func TestIntegrationInvalidMCPJSON(t *testing.T) {
	h := newTestEnv(t, true)
	h.installHarness()
	h.write(".mcp.json", "{not json")
	h.write(".claude/commands/ralph/import.md", "x")
	_, errOut, err := h.run("", "status")
	if err != nil || !strings.Contains(errOut, "could not check ralph's integration files:") {
		t.Errorf("err=%v stderr=%q", err, errOut)
	}
}

func TestIntegrationChecksOncePerCommandWithSubcommands(t *testing.T) {
	h := staleEnv(t)
	_, errOut, err := h.run("", "task", "list")
	if err != nil || strings.Count(errOut, outOfDateWarn) != 1 {
		t.Errorf("err=%v stderr=%q", err, errOut)
	}
}

func TestIntegrationNotice(t *testing.T) {
	stale := []harness.Change{{Path: "a.md"}, {Path: "b.md"}}
	updated := &integrationState{result: setup.CheckResult{Stale: stale}, Updated: stale}
	manual := &integrationState{result: setup.CheckResult{Stale: stale}, mode: "manual"}
	wantUpdated := "ralph updated its integration files in this repository: a.md, b.md. " +
		"They are uncommitted. Tell the user to commit them before running any loop (ralph loop)."
	wantStale := "ralph's integration files in this repository are out of date: a.md, b.md. " +
		"Tell the user to run `ralph init` (and commit the result) before running any loop."

	cases := []struct {
		name       string
		s          *integrationState
		locked     bool
		instr, err string
	}{
		{"updated", updated, false, wantUpdated, wantUpdated},
		{"stale", manual, false, wantStale, wantStale},
		{"updated locked", updated, true, "", wantUpdated},
		{"stale locked", manual, true, "", wantStale},
		{"nothing", &integrationState{}, false, "", ""},
		{"nil", nil, false, "", ""},
		{"check error", &integrationState{err: os.ErrInvalid, result: setup.CheckResult{Stale: stale}}, false, "", ""},
	}
	for _, c := range cases {
		instr, errLine := integrationNotice(c.s, c.locked)
		if instr != c.instr || errLine != c.err {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", c.name, instr, errLine, c.instr, c.err)
		}
	}
}

func TestIntegrationPreRunWritesNothingToStdout(t *testing.T) {
	for _, mode := range []string{"manual", "auto"} {
		h := staleEnv(t)
		t.Setenv("RALPH_INIT__UPDATE", mode)
		plain := func() string {
			h2 := newTestEnv(t, true)
			h2.installHarness()
			o, _, _ := h2.run("", "status")
			return o
		}()
		out, errOut, err := h.run("", "status")
		if err != nil || out != plain || !strings.Contains(errOut, staleCmd) || strings.Contains(out, "integration") {
			t.Errorf("%s: err=%v stdout=%q stderr=%q", mode, err, out, errOut)
		}
	}
}
