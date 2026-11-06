package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/embed/ollama"
	"github.com/bengearig/ralph/internal/harness"
	"github.com/bengearig/ralph/internal/project"
	"github.com/bengearig/ralph/internal/store/sqlite"

	// Register harness implementations.
	_ "github.com/bengearig/ralph/internal/harness/claudecode"
)

func (a *app) newInitCommand() *cobra.Command {
	var force, noHarness bool
	var ralphCommand string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up ralph in this repository",
		Long: `Creates .ralph/ (config template and database), ignores the database in git, and
wires the configured harness into the project. For Claude Code that means
registering the MCP server in .mcp.json, allowing its tools in
.claude/settings.json, installing the /ralph:* slash commands, and adding a
RALPH section to CLAUDE.md. Safe to rerun; files you customized are left alone
unless --force is given.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := a.initRoot(cmd.Context())
			if err != nil {
				return err
			}
			p := project.Project{Root: root}
			var changes []harness.Change

			if err := os.MkdirAll(p.Dir(), 0o755); err != nil {
				return err
			}
			c, err := writeIfAbsent(root, p.ConfigPath(), config.ProjectTemplate(), force)
			if err != nil {
				return err
			}
			changes = append(changes, c)

			a.proj, a.projErr = &p, nil
			l, err := a.loadConfig()
			if err != nil {
				return err
			}
			cfg := l.Config

			dbPath := cfg.StoragePath(root)
			c = harness.Change{Path: relPath(root, dbPath), Action: harness.Unchanged}
			if _, err := os.Stat(dbPath); errors.Is(err, fs.ErrNotExist) {
				c.Action = harness.Created
			}
			st, err := sqlite.Open(cmd.Context(), dbPath)
			if err != nil {
				return err
			}
			if err := st.Close(); err != nil {
				return err
			}
			changes = append(changes, c)

			c, err = ensureGitignore(root, dbPath)
			if err != nil {
				return err
			}
			changes = append(changes, c)

			if !noHarness {
				h, err := harness.New(cfg.Harness.Default, cfg)
				if err != nil {
					return err
				}
				inst, ok := h.(harness.Installer)
				if !ok {
					a.warn(fmt.Sprintf("harness %s has no project integration to install", h.Name()))
				} else {
					hc, err := inst.Install(root, harness.InstallOptions{RalphCommand: ralphCommand, Force: force})
					changes = append(changes, hc...)
					if err != nil {
						return err
					}
				}
			}

			hints := a.initHints(cmd.Context(), cfg)
			if a.jsonOut {
				return a.writeJSON(struct {
					Root    string           `json:"root"`
					Changes []harness.Change `json:"changes"`
					Hints   []string         `json:"hints,omitempty"`
				}{root, changes, hints})
			}
			if err := a.printf("initialized ralph in %s\n\n", root); err != nil {
				return err
			}
			rows := make([][]string, 0, len(changes))
			for _, c := range changes {
				rows = append(rows, []string{c.Action, c.Path, c.Note})
			}
			if err := a.writeTable([]string{"ACTION", "PATH", "NOTE"}, rows); err != nil {
				return err
			}
			for _, h := range hints {
				a.warn(h)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite the config template and customized integration files")
	cmd.Flags().BoolVar(&noHarness, "no-harness", false, "skip installing the harness integration")
	cmd.Flags().StringVar(&ralphCommand, "ralph-command", "ralph", "command the harness runs to start `ralph mcp` (use an absolute path if ralph is not on PATH)")
	return cmd
}

// initRoot picks the project root: --project-dir if given, else the git
// work tree root, else the current directory.
func (a *app) initRoot(ctx context.Context) (string, error) {
	if a.projectDir != "" {
		root, err := filepath.Abs(a.projectDir)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(root)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("%s is not a directory", root)
		}
		return root, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	root, err := project.GitRoot(ctx, wd)
	if errors.Is(err, project.ErrNotGitRepo) {
		a.warn("not inside a git repository; initializing in the current directory (the loop and pull requests need git)")
		return wd, nil
	}
	if err != nil {
		a.warn(fmt.Sprintf("could not run git (%v); initializing in the current directory", err))
		return wd, nil
	}
	return root, nil
}

func relPath(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil {
		return r
	}
	return p
}

func writeIfAbsent(root, p string, content []byte, force bool) (harness.Change, error) {
	c := harness.Change{Path: relPath(root, p)}
	_, err := os.Stat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		c.Action = harness.Created
	case err != nil:
		return c, err
	case force:
		c.Action = harness.Updated
	default:
		c.Action = harness.Unchanged
		return c, nil
	}
	return c, os.WriteFile(p, content, 0o644)
}

// ensureGitignore ignores ralph's local files: the database and its WAL
// files (when stored inside the project), loop run logs, and the loop lock.
func ensureGitignore(root, dbPath string) (harness.Change, error) {
	patterns := []string{
		"/" + project.DirName + "/runs/",
		"/" + project.DirName + "/loop.lock",
	}
	if r, err := filepath.Rel(root, dbPath); err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		patterns = append([]string{"/" + filepath.ToSlash(r) + "*"}, patterns...)
	}
	p := filepath.Join(root, ".gitignore")
	c := harness.Change{Path: ".gitignore"}

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

// initHints checks the embedding provider so problems surface at setup time
// rather than as warnings on the first write.
func (a *app) initHints(ctx context.Context, cfg *config.Config) []string {
	if cfg.Embeddings.Provider != config.EmbeddingsOllama {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	c := ollama.New(ollama.Config{BaseURL: cfg.Embeddings.Ollama.BaseURL, Model: cfg.Embeddings.Ollama.Model, Timeout: 3 * time.Second})
	ok, err := c.HasModel(ctx)
	switch {
	case err != nil:
		return []string{fmt.Sprintf("Ollama is not reachable at %s (%v); search will use keywords until it is. Start Ollama, then run `ralph index`.",
			cfg.Embeddings.Ollama.BaseURL, err)}
	case !ok:
		return []string{fmt.Sprintf("embedding model %q is not installed; run `ollama pull %s`, then `ralph index`.",
			c.ModelName(), c.ModelName())}
	}
	return nil
}
