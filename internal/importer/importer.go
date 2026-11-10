// Package importer reads files and directories into the form the import
// queue accepts, applying the project boundary, ignore rules, and size and
// text checks.
package importer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/bengearig/ralph/internal/git"
	"github.com/bengearig/ralph/internal/project"
	"github.com/bengearig/ralph/internal/service"
)

// Input is a file or directory to import, relative to the current directory
// or absolute.
type Input struct {
	Path string
	Dir  bool
}

// Skipped is a file or input that was not queued, with the reason.
type Skipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

const (
	reasonNotFound   = "not found"
	reasonOutside    = "outside the project"
	reasonSymlink    = "symlink"
	reasonNotRegular = "not a regular file"
	reasonInternal   = "ralph or git internal file"
)

type collector struct {
	root     string
	maxBytes int
	files    map[string]string
	skipped  map[string]string
}

// Collect reads the inputs under the project root. Problems with an
// individual input or file are reported in skipped; err is reserved for
// failures that stop the whole collection.
func Collect(ctx context.Context, root string, inputs []Input, maxBytes int) (files []service.ImportFile, skipped []Skipped, warnings []string, err error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolving project root: %w", err)
	}
	c := &collector{root: realRoot, maxBytes: maxBytes, files: map[string]string{}, skipped: map[string]string{}}

	var repo *git.Repo
	gitChecked, warned := false, false
	for _, in := range inputs {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		abs, rel, ok := c.resolve(in.Path)
		if !ok {
			continue
		}
		info, err := os.Lstat(abs)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			c.skip(rel, reasonNotFound)
			continue
		case err != nil:
			c.skip(rel, "unreadable: "+err.Error())
			continue
		case info.Mode()&fs.ModeSymlink != 0:
			c.skip(rel, reasonSymlink)
			continue
		case !in.Dir && info.IsDir():
			c.skip(rel, "is a directory; use --dir")
			continue
		case in.Dir && !info.IsDir():
			c.skip(rel, "not a directory")
			continue
		}
		if !in.Dir {
			c.addFile(abs, rel, true)
			continue
		}
		if !gitChecked {
			gitChecked = true
			gitRoot, gerr := project.GitRoot(ctx, c.root)
			switch {
			case gerr == nil:
				repo = &git.Repo{Dir: gitRoot}
			case errors.Is(gerr, project.ErrNotGitRepo), errors.Is(gerr, exec.ErrNotFound):
			default:
				return nil, nil, nil, gerr
			}
		}
		if repo == nil {
			if !warned {
				warned = true
				warnings = append(warnings, "not in a git work tree; .gitignore rules were not applied")
			}
			if err := c.walk(ctx, abs); err != nil {
				return nil, nil, nil, err
			}
			continue
		}
		if err := c.list(ctx, repo, abs); err != nil {
			return nil, nil, nil, err
		}
	}

	for p := range c.files {
		content, err := c.read(p)
		if err != nil {
			c.skip(p, err.Error())
			delete(c.files, p)
			continue
		}
		c.files[p] = content
	}
	files = make([]service.ImportFile, 0, len(c.files))
	for p, content := range c.files {
		files = append(files, service.ImportFile{Path: p, Content: content})
	}
	slices.SortFunc(files, func(a, b service.ImportFile) int { return strings.Compare(a.Path, b.Path) })
	skipped = make([]Skipped, 0, len(c.skipped))
	for p, reason := range c.skipped {
		skipped = append(skipped, Skipped{Path: p, Reason: reason})
	}
	slices.SortFunc(skipped, func(a, b Skipped) int { return strings.Compare(a.Path, b.Path) })
	return files, skipped, warnings, nil
}

func (c *collector) skip(rel, reason string) {
	if _, dup := c.skipped[rel]; !dup {
		c.skipped[rel] = reason
	}
}

// resolve returns the input's absolute path with its parent's symlinks
// resolved, and its project-relative path. ok is false when the input was
// recorded as skipped instead.
func (c *collector) resolve(in string) (abs, rel string, ok bool) {
	given := filepath.ToSlash(in)
	a, err := filepath.Abs(in)
	if err != nil {
		c.skip(given, "unreadable: "+err.Error())
		return "", "", false
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(a))
	if err != nil {
		c.skip(given, reasonNotFound)
		return "", "", false
	}
	abs = filepath.Join(parent, filepath.Base(a))
	rel, inside := c.relative(abs)
	if !inside {
		c.skip(given, reasonOutside)
		return "", "", false
	}
	return abs, rel, true
}

func (c *collector) relative(abs string) (string, bool) {
	rel, err := filepath.Rel(c.root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func internalPath(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if part == ".git" || part == project.DirName {
			return true
		}
	}
	return false
}

// addFile registers a file candidate. Entries found by directory listing
// that are internal or deleted are dropped silently; explicit inputs are
// reported.
func (c *collector) addFile(abs, rel string, explicit bool) {
	if rel == "." {
		c.skip(rel, "not a regular file")
		return
	}
	if internalPath(rel) {
		if explicit {
			c.skip(rel, reasonInternal)
		}
		return
	}
	if err := service.ValidateImportPath(rel); err != nil {
		c.skip(rel, "unsupported path")
		return
	}
	info, err := os.Lstat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// Tracked but deleted from the work tree.
		if explicit {
			c.skip(rel, reasonNotFound)
		}
		return
	case err != nil:
		c.skip(rel, "unreadable: "+err.Error())
		return
	case info.Mode()&fs.ModeSymlink != 0:
		c.skip(rel, reasonSymlink)
		return
	case !info.Mode().IsRegular():
		c.skip(rel, reasonNotRegular)
		return
	case info.Size() > int64(c.maxBytes):
		c.skip(rel, fmt.Sprintf("too large (%d bytes, limit %d)", info.Size(), c.maxBytes))
		return
	}
	c.files[rel] = ""
}

func (c *collector) walk(ctx context.Context, dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		rel, _ := c.relative(p)
		if err != nil {
			c.skip(rel, "unreadable: "+err.Error())
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if p != dir && (d.Name() == ".git" || d.Name() == project.DirName) {
				return fs.SkipDir
			}
			return nil
		}
		c.addFile(p, rel, false)
		return nil
	})
}

func (c *collector) list(ctx context.Context, repo *git.Repo, dir string) error {
	rel, err := filepath.Rel(repo.Dir, dir)
	if err != nil {
		return err
	}
	paths, err := repo.ListFiles(ctx, filepath.ToSlash(rel))
	if err != nil {
		return err
	}
	for _, p := range paths {
		abs := filepath.Join(repo.Dir, filepath.FromSlash(p))
		pr, inside := c.relative(abs)
		if !inside {
			continue
		}
		c.addFile(abs, pr, false)
	}
	return nil
}

// read returns the file's content, or an error whose text is the skip reason.
func (c *collector) read(rel string) (string, error) {
	f, err := os.Open(filepath.Join(c.root, filepath.FromSlash(rel)))
	if err != nil {
		return "", fmt.Errorf("unreadable: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(c.maxBytes)+1))
	if err != nil {
		return "", fmt.Errorf("unreadable: %w", err)
	}
	switch {
	case len(data) > c.maxBytes:
		return "", fmt.Errorf("too large (over %d bytes)", c.maxBytes)
	case strings.IndexByte(string(data), 0) >= 0:
		return "", errors.New("not text (contains a NUL byte)")
	case !utf8.Valid(data):
		return "", errors.New("not text (invalid UTF-8)")
	}
	return string(data), nil
}
