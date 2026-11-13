package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/progress"
)

func progressApp(t *testing.T, s Streams, term string, cfg string) *app {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	t.Setenv("HOME", dir)
	t.Setenv("RALPH_EMBEDDINGS__PROVIDER", "none")
	a := &app{streams: s, projectDir: dir, getTerm: func() string { return term }}
	a.root = a.newRootCommand()
	if cfg != "" {
		path := filepath.Join(dir, "extra.yaml")
		if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		a.configFile = path
	}
	return a
}

func TestProgressMode(t *testing.T) {
	tests := []struct {
		name       string
		streams    Streams
		term       string
		cfg        string
		noProgress bool
		json       bool
		want       progress.Mode
	}{
		{"no-progress flag", Streams{ErrIsTerminal: true}, "xterm-256color", "", true, false, progress.Off},
		{"json", Streams{ErrIsTerminal: true}, "xterm-256color", "", false, true, progress.Off},
		{"ui.progress never", Streams{ErrIsTerminal: true}, "xterm-256color", "ui:\n  progress: never\n", false, false, progress.Off},
		{"terminal", Streams{ErrIsTerminal: true}, "xterm-256color", "", false, false, progress.Live},
		{"dumb terminal", Streams{ErrIsTerminal: true}, "dumb", "", false, false, progress.Plain},
		{"not a terminal", Streams{}, "xterm-256color", "", false, false, progress.Plain},
		{"stdin terminal only", Streams{IsTerminal: true}, "xterm-256color", "", false, false, progress.Plain},
		{"config load fails", Streams{ErrIsTerminal: true}, "xterm-256color", "ui: [unterminated\n", false, false, progress.Live},
		{"config load fails off terminal", Streams{}, "xterm-256color", "ui: [unterminated\n", false, false, progress.Plain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := progressApp(t, tt.streams, tt.term, tt.cfg)
			a.jsonOut = tt.json
			if got := a.progressMode(tt.noProgress); got != tt.want {
				t.Errorf("progressMode = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAddNoProgressFlag(t *testing.T) {
	cmd := &cobra.Command{Use: "x"}
	var v bool
	addNoProgressFlag(cmd, &v)
	f := cmd.Flags().Lookup("no-progress")
	if f == nil {
		t.Fatal("--no-progress not registered")
	}
	if f.Usage != "do not show progress" || f.DefValue != "false" {
		t.Errorf("usage = %q, default = %q", f.Usage, f.DefValue)
	}
	if err := cmd.ParseFlags([]string{"--no-progress"}); err != nil || !v {
		t.Errorf("parse: err = %v, value = %v", err, v)
	}
}

func TestNewProgressWritesOnlyToErr(t *testing.T) {
	var out, errOut bytes.Buffer
	a := progressApp(t, Streams{Out: &out, Err: &errOut}, "xterm", "")
	r := a.newProgress(false)
	r.Start("Syncing", 2)
	r.Step("one")
	r.Step("two")
	r.Finish()
	if out.Len() != 0 {
		t.Errorf("Out got %q", out.String())
	}
	if errOut.Len() == 0 {
		t.Error("nothing written to Err in plain mode")
	}
}
