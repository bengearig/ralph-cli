package selfupdate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func assertNoTemp(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".update-") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
}

func stageWrite(t *testing.T, target, content string) *Staged {
	t.Helper()
	s, err := Stage(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Writer().Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCommitReplacesContentAndKeepsMode(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ralph")
	writeFile(t, target, "old", 0o750)

	s := stageWrite(t, target, "new")
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, target); got != "new" {
		t.Errorf("content = %q", got)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o750 {
		t.Errorf("mode = %v", info.Mode().Perm())
	}
	assertNoTemp(t, dir)
}

func TestCommitAddsExecuteBits(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ralph")
	writeFile(t, target, "old", 0o644)

	if err := stageWrite(t, target, "new").Commit(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v", info.Mode().Perm())
	}
}

func TestAbort(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ralph")
	writeFile(t, target, "old", 0o755)

	s := stageWrite(t, target, "new")
	s.Abort()
	s.Abort()
	if got := readFile(t, target); got != "old" {
		t.Errorf("content = %q", got)
	}
	assertNoTemp(t, dir)

	s = stageWrite(t, target, "new")
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	s.Abort()
	if got := readFile(t, target); got != "new" {
		t.Errorf("content after abort = %q", got)
	}
	assertNoTemp(t, dir)
}

func TestSymlink(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "real")
	link := filepath.Join(dir, "link")
	writeFile(t, file, "old", 0o755)
	if err := os.Symlink(file, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	resolved, err := Resolve(link)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(file)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != want {
		t.Fatalf("Resolve = %q, want %q", resolved, want)
	}
	if err := stageWrite(t, resolved, "new").Commit(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, link); got != "new" {
		t.Errorf("content via link = %q", got)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("link was replaced")
	}
	if dest, err := os.Readlink(link); err != nil || dest != file {
		t.Errorf("link points at %q, %v", dest, err)
	}
}

func TestResolveErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := Resolve(dir); err == nil {
		t.Error("directory: expected error")
	}
	if _, err := Resolve(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing: expected error")
	}
}

func TestStageNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permissions do not apply to root")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "ralph")
	writeFile(t, target, "old", 0o755)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	_, err := Stage(target)
	if !errors.Is(err, ErrNotWritable) {
		t.Fatalf("err = %v, want ErrNotWritable", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error does not name the directory: %v", err)
	}
}

func TestStageMissingTarget(t *testing.T) {
	_, err := Stage(filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, ErrNotWritable) {
		t.Errorf("missing target reported as not writable: %v", err)
	}
}

func TestReplaceAside(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ralph")
	staged := filepath.Join(dir, "staged")
	writeFile(t, target, "old", 0o755)
	writeFile(t, staged, "new", 0o755)

	if err := replaceAside(target, staged); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, target); got != "new" {
		t.Errorf("target = %q", got)
	}
	if got := readFile(t, target+".old"); got != "old" {
		t.Errorf(".old = %q", got)
	}
	if _, err := os.Stat(staged); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("staged file still present: %v", err)
	}
}

func TestReplaceAsideRemovesStaleOld(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ralph")
	staged := filepath.Join(dir, "staged")
	writeFile(t, target, "old", 0o755)
	writeFile(t, target+".old", "stale", 0o755)
	writeFile(t, staged, "new", 0o755)

	if err := replaceAside(target, staged); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, target+".old"); got != "old" {
		t.Errorf(".old = %q", got)
	}
}

func TestReplaceAsideRestoresOnFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ralph")
	writeFile(t, target, "old", 0o755)

	if err := replaceAside(target, filepath.Join(dir, "missing")); err == nil {
		t.Fatal("expected error")
	}
	if got := readFile(t, target); got != "old" {
		t.Errorf("target = %q", got)
	}
	if _, err := os.Stat(target + ".old"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".old present: %v", err)
	}
}

func TestCleanupOld(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ralph")
	CleanupOld(target)
	writeFile(t, target+".old", "old", 0o755)
	CleanupOld(target)
	if _, err := os.Stat(target + ".old"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".old present: %v", err)
	}
}
