package importer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/service"
)

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

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	cmd := exec.Command("git", "init", "--quiet")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return root
}

func gitAdd(t *testing.T, root string, paths ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"add", "--"}, paths...)...)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	t.Chdir(dir)
}

func paths(files []service.ImportFile) []string {
	out := []string{}
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

func reasons(skipped []Skipped) map[string]string {
	out := map[string]string{}
	for _, s := range skipped {
		out[s.Path] = s.Reason
	}
	return out
}

func collect(t *testing.T, root string, max int, inputs ...Input) ([]service.ImportFile, []Skipped, []string) {
	t.Helper()
	files, skipped, warnings, err := Collect(context.Background(), root, inputs, max)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return files, skipped, warnings
}

func TestGitTreeHonorsIgnoreRules(t *testing.T) {
	root := gitRepo(t)
	write(t, root, ".gitignore", "ignored.md\n")
	write(t, root, "docs/tracked.md", "tracked")
	write(t, root, "docs/untracked.md", "untracked")
	write(t, root, "docs/ignored.md", "ignored")
	write(t, root, ".ralph/ralph.db", "db")
	gitAdd(t, root, ".gitignore", "docs/tracked.md")
	chdir(t, root)

	files, skipped, warnings := collect(t, root, 1024, Input{Path: ".", Dir: true})
	want := []string{".gitignore", "docs/tracked.md", "docs/untracked.md"}
	if got := paths(files); !slices.Equal(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
	if len(skipped) != 0 || len(warnings) != 0 {
		t.Errorf("skipped = %v, warnings = %v", skipped, warnings)
	}
}

func TestGitSubdirectoryProjectRelativePaths(t *testing.T) {
	repo := gitRepo(t)
	root := filepath.Join(repo, "proj")
	write(t, root, "docs/a.md", "a")
	write(t, repo, "outside.md", "x")
	chdir(t, root)

	files, _, _ := collect(t, root, 1024, Input{Path: "docs", Dir: true})
	if got := paths(files); !slices.Equal(got, []string{"docs/a.md"}) {
		t.Errorf("files = %v", got)
	}
}

func TestNonGitWalk(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	write(t, root, "docs/a.md", "a")
	write(t, root, "docs/sub/b.md", "b")
	write(t, root, ".git/config", "x")
	write(t, root, ".ralph/ralph.db", "x")
	write(t, root, "docs/.git/x", "x")
	chdir(t, root)

	files, _, warnings := collect(t, root, 1024, Input{Path: ".", Dir: true}, Input{Path: "docs", Dir: true})
	if got := paths(files); !slices.Equal(got, []string{"docs/a.md", "docs/sub/b.md"}) {
		t.Errorf("files = %v", got)
	}
	if len(warnings) != 1 {
		t.Errorf("warnings = %v, want exactly one", warnings)
	}
}

func TestNonGitFileOnlyHasNoWarning(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	write(t, root, "a.md", "a")
	chdir(t, root)
	_, _, warnings := collect(t, root, 1024, Input{Path: "a.md"})
	if len(warnings) != 0 {
		t.Errorf("warnings = %v", warnings)
	}
}

func TestInternalFilesNeverIncluded(t *testing.T) {
	root := gitRepo(t)
	write(t, root, ".ralph/notes.md", "x")
	write(t, root, "a.md", "a")
	gitAdd(t, root, "a.md")
	cmd := exec.Command("git", "add", "-f", "--", ".ralph/notes.md")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add -f: %v\n%s", err, out)
	}
	chdir(t, root)

	files, skipped, _ := collect(t, root, 1024, Input{Path: ".", Dir: true}, Input{Path: ".ralph/notes.md"})
	if got := paths(files); !slices.Equal(got, []string{"a.md"}) {
		t.Errorf("files = %v", got)
	}
	if r := reasons(skipped)[".ralph/notes.md"]; r != reasonInternal {
		t.Errorf("reason = %q", r)
	}
}

func TestOutsideProjectRefused(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	root := filepath.Join(base, "proj")
	write(t, root, "a.md", "a")
	write(t, base, "other/secret.md", "s")
	chdir(t, root)

	files, skipped, _ := collect(t, root, 1024,
		Input{Path: filepath.Join(base, "other", "secret.md")},
		Input{Path: "../other", Dir: true},
		Input{Path: "a.md"})
	if got := paths(files); !slices.Equal(got, []string{"a.md"}) {
		t.Errorf("files = %v", got)
	}
	r := reasons(skipped)
	if len(r) != 2 {
		t.Fatalf("skipped = %v", skipped)
	}
	for p, reason := range r {
		if reason != reasonOutside {
			t.Errorf("%s: reason = %q", p, reason)
		}
	}
}

func TestSymlinkEscapingProjectRefused(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	root := filepath.Join(base, "proj")
	write(t, root, "a.md", "a")
	write(t, base, "other/secret.md", "s")
	if err := os.Symlink(filepath.Join(base, "other"), filepath.Join(root, "link")); err != nil {
		t.Skip("symlinks unavailable")
	}
	chdir(t, root)

	files, skipped, _ := collect(t, root, 1024, Input{Path: "link/secret.md"})
	if len(files) != 0 || reasons(skipped)["link/secret.md"] != reasonOutside {
		t.Errorf("files = %v, skipped = %v", files, skipped)
	}
}

func TestSymlinksSkipped(t *testing.T) {
	root := gitRepo(t)
	write(t, root, "docs/a.md", "a")
	write(t, root, "docs/real/x.md", "x")
	if err := os.Symlink("a.md", filepath.Join(root, "docs", "alias.md")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if err := os.Symlink("real", filepath.Join(root, "docs", "dirlink")); err != nil {
		t.Fatal(err)
	}
	chdir(t, root)

	files, skipped, _ := collect(t, root, 1024, Input{Path: "docs", Dir: true}, Input{Path: "docs/alias.md"})
	if got := paths(files); !slices.Equal(got, []string{"docs/a.md", "docs/real/x.md"}) {
		t.Errorf("files = %v", got)
	}
	r := reasons(skipped)
	if r["docs/alias.md"] != reasonSymlink || r["docs/dirlink"] != reasonSymlink {
		t.Errorf("skipped = %v", skipped)
	}

	plain, _ := filepath.EvalSymlinks(t.TempDir())
	write(t, plain, "real/x.md", "x")
	_ = os.Symlink("real", filepath.Join(plain, "dirlink"))
	chdir(t, plain)
	files, skipped, _ = collect(t, plain, 1024, Input{Path: ".", Dir: true})
	if got := paths(files); !slices.Equal(got, []string{"real/x.md"}) {
		t.Errorf("walk files = %v", got)
	}
	if reasons(skipped)["dirlink"] != reasonSymlink {
		t.Errorf("walk skipped = %v", skipped)
	}
}

func TestNotTextAndTooLarge(t *testing.T) {
	root := gitRepo(t)
	write(t, root, "big.md", strings.Repeat("x", 11))
	write(t, root, "latin1.md", "caf\xe9")
	write(t, root, "nul.md", "a\x00b")
	write(t, root, "empty.md", "")
	write(t, root, "ok.md", "ok")
	chdir(t, root)

	files, skipped, _ := collect(t, root, 10, Input{Path: ".", Dir: true})
	if got := paths(files); !slices.Equal(got, []string{"empty.md", "ok.md"}) {
		t.Errorf("files = %v", got)
	}
	if files[0].Content != "" {
		t.Errorf("empty file content = %q", files[0].Content)
	}
	r := reasons(skipped)
	seen := map[string]bool{}
	for _, p := range []string{"big.md", "latin1.md", "nul.md"} {
		if r[p] == "" || seen[r[p]] {
			t.Errorf("%s: missing or duplicate reason %q in %v", p, r[p], r)
		}
		seen[r[p]] = true
	}
	if !strings.Contains(r["big.md"], "too large") || !strings.Contains(r["latin1.md"], "UTF-8") || !strings.Contains(r["nul.md"], "NUL") {
		t.Errorf("reasons = %v", r)
	}
}

func TestMissingInput(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	write(t, root, "a.md", "a")
	chdir(t, root)

	files, skipped, _ := collect(t, root, 1024,
		Input{Path: "nope.md"}, Input{Path: "nodir", Dir: true}, Input{Path: "no/such/deep.md"}, Input{Path: "a.md"})
	if len(files) != 1 {
		t.Errorf("files = %v", files)
	}
	r := reasons(skipped)
	for _, p := range []string{"nope.md", "nodir", "no/such/deep.md"} {
		if r[p] != reasonNotFound {
			t.Errorf("%s: reason = %q", p, r[p])
		}
	}
}

func TestKindMismatch(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	write(t, root, "docs/a.md", "a")
	chdir(t, root)
	files, skipped, _ := collect(t, root, 1024, Input{Path: "docs"}, Input{Path: "docs/a.md", Dir: true})
	if len(files) != 0 || len(skipped) != 2 {
		t.Errorf("files = %v, skipped = %v", files, skipped)
	}
}

func TestDedupeAndSort(t *testing.T) {
	root := gitRepo(t)
	write(t, root, "docs/b.md", "b")
	write(t, root, "docs/a.md", "a")
	write(t, root, "docs/sub/c.md", "c")
	write(t, root, "z.md", "z")
	chdir(t, root)

	abs := filepath.Join(root, "docs", "b.md")
	files, _, _ := collect(t, root, 1024,
		Input{Path: "z.md"}, Input{Path: "docs", Dir: true}, Input{Path: "docs/sub", Dir: true},
		Input{Path: abs}, Input{Path: "./docs/../docs/a.md"})
	want := []string{"docs/a.md", "docs/b.md", "docs/sub/c.md", "z.md"}
	if got := paths(files); !slices.Equal(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
}

func TestRelativeToCurrentDirectory(t *testing.T) {
	root := gitRepo(t)
	write(t, root, "docs/a.md", "a")
	chdir(t, filepath.Join(root, "docs"))
	files, _, _ := collect(t, root, 1024, Input{Path: "a.md"}, Input{Path: ".", Dir: true})
	if got := paths(files); !slices.Equal(got, []string{"docs/a.md"}) {
		t.Errorf("files = %v", got)
	}
}
