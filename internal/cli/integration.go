package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/harness"
	"github.com/bengearig/ralph/internal/setup"
)

// integrationState is the memoized outcome of the freshness check.
type integrationState struct {
	result setup.CheckResult
	err    error
	mode   string
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
	a.warnIntegration()
	return nil
}

func (a *app) warnIntegration() {
	s := a.integration
	if s.err != nil {
		a.warn(fmt.Sprintf("could not check ralph's integration files: %v", s.err))
		return
	}
	const indent = "\n         "
	if len(s.result.Stale) > 0 {
		a.warn("the integration files in this repository are out of date: " + changePaths(s.result.Stale) +
			indent + "run `ralph init` to update them")
	}
	if len(s.result.Customized) > 0 {
		a.warn("these customized files are left alone: " + changePaths(s.result.Customized) +
			indent + "`ralph init --force` overwrites them; add them to init.ignore to stop this warning")
	}
}

func changePaths(cs []harness.Change) string {
	paths := make([]string, len(cs))
	for i, c := range cs {
		paths[i] = c.Path
	}
	return strings.Join(paths, ", ")
}
