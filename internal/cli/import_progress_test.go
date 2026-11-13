package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/errs"
)

const gitWarning = "warning: not in a git work tree; .gitignore rules were not applied\n"

func withoutGitWarning(s string) string { return strings.ReplaceAll(s, gitWarning, "") }

const queuePlainProgress = "queueing files: 2 items\n[1/2] docs/a.md\n[2/2] docs/b.md\n"

func TestImportPlainProgress(t *testing.T) {
	h := importFixture(t)
	out, errOut, err := h.run("", "import", "--dir", "docs")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	errOut = withoutGitWarning(errOut)
	if errOut != queuePlainProgress {
		t.Errorf("stderr = %q", errOut)
	}
	if strings.ContainsAny(errOut, "\r\x1b") {
		t.Errorf("control characters in %q", errOut)
	}
	if !strings.Contains(out, "new (2):\n  docs/a.md\n  docs/b.md\n") || strings.Contains(out, "queueing") {
		t.Errorf("stdout = %q", out)
	}
}

func TestImportAsNewPlainProgress(t *testing.T) {
	h := importFixture(t)
	out, errOut, err := h.run("", "import", "--as-new", "--file", "docs/a.md", "--file", "docs/b.md")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if want := "importing files: 2 items\n[1/2] docs/a.md\n[2/2] docs/b.md\n"; errOut != want {
		t.Errorf("stderr = %q", errOut)
	}
	if !strings.Contains(out, "created (2):\n  docs/a.md -> D-1\n  docs/b.md -> D-2\n") {
		t.Errorf("stdout = %q", out)
	}

	if err := os.WriteFile(filepath.Join(h.dir, "docs", "a.md"), []byte("# A2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errOut, err = h.run("", "import", "--as-new", "--file", "docs/a.md", "--file", "docs/b.md")
	if !exitIs(err, errs.ErrNeedsHuman) {
		t.Fatalf("err = %v", err)
	}
	if want := "importing files: 2 items\n[1/2] docs/a.md\n[2/2] docs/b.md\n"; errOut != want {
		t.Errorf("stderr = %q", errOut)
	}
	if !strings.Contains(out, "refused (1):\n  docs/a.md: changed since its last import") || !strings.Contains(out, "skipped (1):") {
		t.Errorf("stdout = %q", out)
	}
}

func TestImportLiveProgress(t *testing.T) {
	for _, args := range [][]string{{"import", "--dir", "docs"}, {"import", "--as-new", "--dir", "docs"}} {
		h := importFixture(t)
		t.Setenv("TERM", "xterm")
		out, errOut, err := h.runErrTerminal(args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !strings.HasSuffix(errOut, "\r\x1b[2K"+gitWarning) {
			t.Errorf("line not cleared before the warning: %q", errOut)
		}
		errOut = strings.TrimSuffix(errOut, gitWarning)
		if !strings.Contains(errOut, "\r\x1b[2K") || !strings.Contains(errOut, "█") || !strings.Contains(errOut, "2/2") || !strings.Contains(errOut, "docs/b.md") {
			t.Errorf("stderr = %q", errOut)
		}
		if !strings.HasSuffix(errOut, "\r\x1b[2K") {
			t.Errorf("line not cleared at the end: %q", errOut)
		}
		if !strings.Contains(out, "docs/a.md") || strings.ContainsAny(out, "\r\x1b") {
			t.Errorf("stdout = %q", out)
		}
	}
}

func TestImportProgressDisabled(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		cfg  bool
	}{
		{"flag", []string{"import", "--dir", "docs", "--no-progress"}, false},
		{"flag as-new", []string{"import", "--as-new", "--dir", "docs", "--no-progress"}, false},
		{"json", []string{"--json", "import", "--dir", "docs"}, false},
		{"json as-new", []string{"--json", "import", "--as-new", "--dir", "docs"}, false},
		{"setting", []string{"import", "--dir", "docs"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, terminal := range []bool{false, true} {
				h := importFixture(t)
				t.Setenv("TERM", "xterm")
				if tc.cfg {
					t.Setenv("RALPH_UI__PROGRESS", "never")
				}
				var out, errOut bytes.Buffer
				full := append([]string{"--project-dir", h.dir}, tc.args...)
				err := Execute(context.Background(), Streams{In: strings.NewReader(""), Out: &out, Err: &errOut, ErrIsTerminal: terminal}, full)
				if err != nil {
					t.Fatalf("import: %v", err)
				}
				if got := withoutGitWarning(errOut.String()); got != "" {
					t.Errorf("terminal=%v: stderr = %q", terminal, got)
				}
				if tc.args[0] == "--json" && !json.Valid(out.Bytes()) {
					t.Errorf("invalid JSON: %q", out.String())
				}
			}
		})
	}
}

func TestImportNothingCollectedNoProgress(t *testing.T) {
	h := importFixture(t)
	for _, args := range [][]string{{"import", "--file", "docs/blob.bin"}, {"import", "--as-new", "--file", "docs/blob.bin"}} {
		out, errOut, err := h.run("", args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if errOut != "" {
			t.Errorf("%v: stderr = %q", args, errOut)
		}
		if !strings.Contains(out, "docs/blob.bin: not text") {
			t.Errorf("%v: stdout = %q", args, out)
		}
	}
}

func TestImportWarningsAfterProgress(t *testing.T) {
	h := importFixture(t)
	t.Setenv("TERM", "xterm")
	_, errOut, err := h.runErrTerminal("import", "--dir", "docs")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if !strings.HasSuffix(errOut, "2/2  docs/b.md\r\x1b[2K"+gitWarning) {
		t.Errorf("warning not after the cleared line: %q", errOut)
	}
}
