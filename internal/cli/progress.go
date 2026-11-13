package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/progress"
)

func addNoProgressFlag(cmd *cobra.Command, dst *bool) {
	cmd.Flags().BoolVar(dst, "no-progress", false, "do not show progress")
}

func (a *app) progressMode(noProgress bool) progress.Mode {
	if noProgress || a.jsonOut {
		return progress.Off
	}
	if l, err := a.loadConfig(); err == nil && l.Config.UI.Progress == config.UIProgressNever {
		return progress.Off
	}
	if a.streams.ErrIsTerminal && a.termEnv() != "dumb" {
		return progress.Live
	}
	return progress.Plain
}

func (a *app) newProgress(noProgress bool) *progress.Reporter {
	return progress.New(a.streams.Err, a.progressMode(noProgress), progress.WidthFromEnv(os.Getenv))
}

func (a *app) termEnv() string {
	if a.getTerm != nil {
		return a.getTerm()
	}
	return os.Getenv("TERM")
}
