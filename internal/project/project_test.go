package project

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLocateWalksUp(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := Locate(deep)
	if err != nil {
		t.Fatal(err)
	}
	if p.Root != root {
		t.Errorf("Root = %q, want %q", p.Root, root)
	}
	if p.ConfigPath() != filepath.Join(root, ".ralph", "config.yaml") {
		t.Errorf("ConfigPath = %q", p.ConfigPath())
	}
}

func TestLocateNotFound(t *testing.T) {
	if _, err := Locate(t.TempDir()); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestLocateIgnoresRalphFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, DirName), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := At(root); !errors.Is(err, ErrNotFound) {
		t.Errorf("a .ralph file is not a project: %v", err)
	}
}

func TestGitRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	plain := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(plain))
	if _, err := GitRoot(ctx, plain); !errors.Is(err, ErrNotGitRepo) {
		t.Errorf("plain dir: %v", err)
	}

	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	sub := filepath.Join(repo, "x")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := GitRoot(ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(repo)
	if gotEval, _ := filepath.EvalSymlinks(got); gotEval != want {
		t.Errorf("GitRoot = %q, want %q", got, want)
	}
}
