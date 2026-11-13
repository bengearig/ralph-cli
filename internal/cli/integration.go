package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/harness"
	"github.com/bengearig/ralph/internal/looplock"
	"github.com/bengearig/ralph/internal/project"
	"github.com/bengearig/ralph/internal/setup"
)

const integrationIndent = "\n         "

// integrationState is the memoized outcome of the freshness check.
type integrationState struct {
	result setup.CheckResult
	err    error
	mode   string
	// Updated lists the files the automatic update created or updated.
	Updated []harness.Change
}

// skipsIntegrationCheck reports whether cmd is exempt from the freshness check.
func skipsIntegrationCheck(cmd *cobra.Command) bool {
	for c := cmd; c != nil && c.HasParent(); c = c.Parent() {
		if !c.Parent().HasParent() {
			switch c.Name() {
			case "init", "version", "help", "completion", cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd:
				return true
			}
		}
	}
	return false
}

// checkIntegration warns when the repository's integration files are stale.
// It runs at most once per process and never fails the command.
func (a *app) checkIntegration(cmd *cobra.Command, _ []string) error {
	if a.integration != nil || skipsIntegrationCheck(cmd) {
		return nil
	}
	p, err := a.findProject()
	if err != nil {
		return nil
	}
	l, err := a.loadConfig()
	if err != nil {
		return nil
	}
	res, err := setup.Check(cmd.Context(), p.Root, l.Config)
	a.integration = &integrationState{result: res, err: err, mode: l.Config.Init.Update}
	if err == nil && len(res.Stale) > 0 && a.canAutoUpdate(cmd, p.Root, l.Config) {
		a.updateIntegration(cmd, p.Root, l.Config)
		return nil
	}
	a.warnIntegration()
	return nil
}

// canAutoUpdate reports whether the update may be applied rather than warned about.
func (a *app) canAutoUpdate(cmd *cobra.Command, root string, cfg *config.Config) bool {
	if cfg.Init.Update != "auto" {
		return false
	}
	if dry, err := cmd.Flags().GetBool("dry-run"); err == nil && dry {
		return false
	}
	_, held, err := looplock.Probe(filepath.Join(root, project.DirName, "loop.lock"))
	return err == nil && !held
}

// updateIntegration rewrites the stale integration files and reports on
// standard error. A failure is a warning.
func (a *app) updateIntegration(cmd *cobra.Command, root string, cfg *config.Config) {
	changes, err := setup.Run(cmd.Context(), setup.Options{
		Root:            root,
		Config:          cfg,
		KeepMCPCommand:  true,
		IntegrationOnly: true,
		Ignore:          cfg.Init.Ignore,
	})
	for _, c := range changes {
		if c.Action == harness.Created || c.Action == harness.Updated {
			a.integration.Updated = append(a.integration.Updated, c)
		}
	}
	if len(a.integration.Updated) > 0 {
		parts := make([]string, len(a.integration.Updated))
		for i, c := range a.integration.Updated {
			parts[i] = fmt.Sprintf("%s (%s)", c.Path, c.Action)
		}
		a.warn("updated its integration files: " + strings.Join(parts, ", ") + integrationIndent + "review and commit them")
	}
	if err != nil {
		a.warn(fmt.Sprintf("could not update ralph's integration files: %v", err))
	}
	a.warnCustomized()
}

func (a *app) warnIntegration() {
	s := a.integration
	if s.err != nil {
		a.warn(fmt.Sprintf("could not check ralph's integration files: %v", s.err))
		return
	}
	if len(s.result.Stale) > 0 {
		a.warn("the integration files in this repository are out of date: " + changePaths(s.result.Stale) +
			integrationIndent + "run `ralph init` to update them")
	}
	a.warnCustomized()
}

func (a *app) warnCustomized() {
	s := a.integration
	if len(s.result.Customized) > 0 {
		a.warn("these customized files are left alone: " + changePaths(s.result.Customized) +
			integrationIndent + "`ralph init --force` overwrites them; add them to init.ignore to stop this warning")
	}
}

func changePaths(cs []harness.Change) string {
	paths := make([]string, len(cs))
	for i, c := range cs {
		paths[i] = c.Path
	}
	return strings.Join(paths, ", ")
}

// loopLockHeld reports whether a loop holds the project lock; a lock that
// cannot be probed counts as held.
func (a *app) loopLockHeld(root string) bool {
	_, held, err := looplock.Probe(filepath.Join(root, project.DirName, "loop.lock"))
	return err != nil || held
}

// integrationNotice builds the text for the person about the integration
// files: the server instructions notice (empty while a loop holds the lock,
// so a headless agent is not prompted to touch the repository) and the line
// for standard error.
func integrationNotice(s *integrationState, loopLocked bool) (instructions, stderr string) {
	if s == nil || s.err != nil {
		return "", ""
	}
	switch {
	case len(s.Updated) > 0:
		paths := make([]string, len(s.Updated))
		for i, c := range s.Updated {
			paths[i] = c.Path
		}
		stderr = "ralph updated its integration files in this repository: " + strings.Join(paths, ", ") +
			". They are uncommitted. Tell the user to commit them before running any loop (ralph loop)."
	case len(s.result.Stale) > 0:
		stderr = "ralph's integration files in this repository are out of date: " + changePaths(s.result.Stale) +
			". Tell the user to run `ralph init` (and commit the result) before running any loop."
	default:
		return "", ""
	}
	if loopLocked {
		return "", stderr
	}
	return stderr, stderr
}
