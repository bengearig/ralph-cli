package cli

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

const (
	staleCmd      = ".claude/commands/ralph/import.md"
	outOfDateWarn = "the integration files in this repository are out of date: "
	customWarn    = "these customized files are left alone: "
)

func staleEnv(t *testing.T) *testEnv {
	t.Helper()
	h := newTestEnv(t, true)
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

func TestIntegrationUpdateModesOnlyWarn(t *testing.T) {
	for _, mode := range []string{"manual", "auto"} {
		t.Run(mode, func(t *testing.T) {
			h := staleEnv(t)
			t.Setenv("RALPH_INIT_UPDATE", mode)
			before := h.snapshot()
			_, errOut, err := h.run("", "status")
			if err != nil || !strings.Contains(errOut, outOfDateWarn+staleCmd) {
				t.Errorf("err=%v stderr=%q", err, errOut)
			}
			if !reflect.DeepEqual(before, h.snapshot()) {
				t.Error("files changed")
			}
		})
	}
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
