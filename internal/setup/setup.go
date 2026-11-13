// Package setup performs the steps of ralph init: the config template, the
// database, .gitignore, and the harness integration. A dry run reports the
// same changes without writing anything.
package setup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/harness"
	"github.com/bengearig/ralph/internal/project"
	"github.com/bengearig/ralph/internal/store/sqlite"
)

const ignoredNote = "ignored by init.ignore"

// Options controls Run.
type Options struct {
	Root string
	// Config is the effective configuration. When nil, LoadConfig supplies it
	// after the config template step, so a template written by this run is
	// part of what is loaded.
	Config     *config.Config
	LoadConfig func() (*config.Config, error)
	// Warn receives non-fatal messages; nil discards them.
	Warn func(string)

	RalphCommand   string
	Force          bool
	NoHarness      bool
	DryRun         bool
	KeepMCPCommand bool
	// Ignore lists project-relative, slash-separated paths to leave alone.
	Ignore []string
}

// Run performs init's steps in order and returns what it did, or with DryRun
// what it would do. On error it returns the changes made so far.
func Run(ctx context.Context, o Options) ([]harness.Change, error) {
	p := project.Project{Root: o.Root}
	var changes []harness.Change

	c, err := configTemplate(o, p)
	if err != nil {
		return changes, err
	}
	changes = append(changes, c)

	cfg := o.Config
	if cfg == nil {
		if o.LoadConfig == nil {
			return changes, errors.New("setup: no configuration")
		}
		if cfg, err = o.LoadConfig(); err != nil {
			return changes, err
		}
	}

	dbPath := cfg.StoragePath(o.Root)
	c, err = database(ctx, o, dbPath)
	if err != nil {
		return changes, err
	}
	changes = append(changes, c)

	c, err = ensureGitignore(o, dbPath)
	if err != nil {
		return changes, err
	}
	changes = append(changes, c)

	if o.NoHarness {
		return changes, nil
	}
	h, err := harness.New(cfg.Harness.Default, cfg)
	if err != nil {
		return changes, err
	}
	inst, ok := h.(harness.Installer)
	if !ok {
		if o.Warn != nil {
			o.Warn(fmt.Sprintf("harness %s has no project integration to install", h.Name()))
		}
		return changes, nil
	}
	hc, err := inst.Install(o.Root, harness.InstallOptions{
		RalphCommand:   o.RalphCommand,
		Force:          o.Force,
		DryRun:         o.DryRun,
		KeepMCPCommand: o.KeepMCPCommand,
		Ignore:         o.Ignore,
	})
	return append(changes, hc...), err
}

func ignored(o Options, rel string) bool {
	return slices.Contains(o.Ignore, filepath.ToSlash(rel))
}

func ignoredChange(rel string) harness.Change {
	return harness.Change{Path: rel, Action: harness.Unchanged, Note: ignoredNote}
}

func relPath(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil {
		return r
	}
	return p
}

func configTemplate(o Options, p project.Project) (harness.Change, error) {
	rel := relPath(o.Root, p.ConfigPath())
	if ignored(o, rel) {
		return ignoredChange(rel), nil
	}
	if !o.DryRun {
		if err := os.MkdirAll(p.Dir(), 0o755); err != nil {
			return harness.Change{}, err
		}
	}
	return writeIfAbsent(o, rel, p.ConfigPath(), config.ProjectTemplate())
}

func writeIfAbsent(o Options, rel, p string, content []byte) (harness.Change, error) {
	c := harness.Change{Path: rel}
	_, err := os.Stat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		c.Action = harness.Created
	case err != nil:
		return c, err
	case o.Force:
		c.Action = harness.Updated
	default:
		c.Action = harness.Unchanged
		return c, nil
	}
	if o.DryRun {
		return c, nil
	}
	return c, os.WriteFile(p, content, 0o644)
}

func database(ctx context.Context, o Options, dbPath string) (harness.Change, error) {
	rel := relPath(o.Root, dbPath)
	if ignored(o, rel) {
		return ignoredChange(rel), nil
	}
	c := harness.Change{Path: rel, Action: harness.Unchanged}
	if _, err := os.Stat(dbPath); errors.Is(err, fs.ErrNotExist) {
		c.Action = harness.Created
	}
	if o.DryRun {
		return c, nil
	}
	st, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		return c, err
	}
	return c, st.Close()
}

// ensureGitignore ignores ralph's local files: the database and its WAL
// files (when stored inside the project), loop run logs, and the loop lock.
func ensureGitignore(o Options, dbPath string) (harness.Change, error) {
	c := harness.Change{Path: ".gitignore"}
	if ignored(o, c.Path) {
		return ignoredChange(c.Path), nil
	}
	patterns := []string{
		"/" + project.DirName + "/runs/",
		"/" + project.DirName + "/loop.lock",
	}
	if r, err := filepath.Rel(o.Root, dbPath); err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		patterns = append([]string{"/" + filepath.ToSlash(r) + "*"}, patterns...)
	}
	p := filepath.Join(o.Root, ".gitignore")

	data, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return c, err
	}
	present := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		present[strings.TrimSpace(sc.Text())] = true
	}
	var missing []string
	for _, pat := range patterns {
		if !present[pat] {
			missing = append(missing, pat)
		}
	}
	if len(missing) == 0 {
		c.Action = harness.Unchanged
		return c, nil
	}
	c.Action = harness.Updated
	if data == nil {
		c.Action = harness.Created
	}
	if o.DryRun {
		return c, nil
	}
	prefix := ""
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		prefix = "\n"
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return c, err
	}
	_, werr := fmt.Fprintf(f, "%s# ralph local files (database, loop logs, loop lock)\n%s\n", prefix, strings.Join(missing, "\n"))
	return c, errors.Join(werr, f.Close())
}
