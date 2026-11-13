package loop

import (
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// tree is the content of a directory, held in memory so a repository built
// once can be restored for each test.
type tree map[string]treeFile

type treeFile struct {
	data []byte
	mode fs.FileMode
}

func readTree(t *testing.T, root string) tree {
	t.Helper()
	files := tree{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			files[rel] = treeFile{mode: info.Mode().Perm() | fs.ModeDir}
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[rel] = treeFile{data: data, mode: info.Mode().Perm()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func restoreTree(t *testing.T, root string, files tree) {
	t.Helper()
	for rel, f := range files {
		path := filepath.Join(root, rel)
		if f.mode.IsDir() {
			if err := os.MkdirAll(path, f.mode.Perm()); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, f.data, f.mode); err != nil {
			t.Fatal(err)
		}
	}
}

var (
	initialOnce sync.Once
	initialTree tree
)

// initialRepo returns a repository on main with one commit, built once per
// test process. It holds no absolute paths, so any directory can host it.
func initialRepo(t *testing.T) tree {
	t.Helper()
	initialOnce.Do(func() {
		root, err := os.MkdirTemp("", "ralph-initial-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(root)
		gitCmd(t, root, "init", "--quiet", "--initial-branch=main")
		if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hi\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitCmd(t, root, "add", ".")
		gitCmd(t, root, "commit", "--quiet", "-m", "initial")
		initialTree = readTree(t, root)
	})
	return initialTree
}

var (
	bareOnce sync.Once
	bareTree tree
)

// bareRepo returns an empty bare repository with main as its initial branch,
// built once per test process. It holds no absolute paths.
func bareRepo(t *testing.T) tree {
	t.Helper()
	bareOnce.Do(func() {
		root, err := os.MkdirTemp("", "ralph-bare-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(root)
		gitCmd(t, root, "init", "--quiet", "--bare", "--initial-branch=main")
		bareTree = readTree(t, root)
	})
	return bareTree
}
