// Package cli implements ralph's command-line interface.
package cli

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/project"
	"github.com/bengearig/ralph/internal/service"
)

// Streams are the standard streams commands read from and write to.
type Streams struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

type app struct {
	streams Streams

	projectDir string
	configFile string
	logLevel   string
	jsonOut    bool

	root    *cobra.Command
	closers []func() error

	proj    *project.Project
	projErr error
	cfg     *config.Loaded
	log     *slog.Logger
	svc     *service.Service
}

// Execute runs the ralph command tree with the given arguments.
func Execute(ctx context.Context, streams Streams, args []string) (err error) {
	a := &app{streams: streams}
	a.root = a.newRootCommand()
	a.root.SetArgs(args)
	a.root.SetIn(streams.In)
	a.root.SetOut(streams.Out)
	a.root.SetErr(streams.Err)
	defer func() { err = errors.Join(err, a.close()) }()
	return a.root.ExecuteContext(ctx)
}

func (a *app) newRootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ralph",
		Short: "Receive, Articulate, Loop, Pull request, Human review",
		Long: `ralph standardizes the RALPH workflow: receive instructions, articulate a
solution as documents, milestones, and tasks, loop an agent over the tasks,
open a pull request, and hand off to humans for review.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	pf := cmd.PersistentFlags()
	pf.StringVar(&a.projectDir, "project-dir", "", "project directory (default: nearest ancestor containing .ralph/)")
	pf.StringVar(&a.configFile, "config", "", "additional config file layered over global and project config")
	pf.StringVar(&a.logLevel, "log-level", "", "log level: debug, info, warn, error")
	pf.BoolVar(&a.jsonOut, "json", false, "write machine-readable JSON output")

	cmd.AddCommand(
		a.newInitCommand(),
		a.newInitiativeCommand(),
		a.newMilestoneCommand(),
		a.newTaskCommand(),
		a.newDocCommand(),
		a.newSearchCommand(),
		a.newIndexCommand(),
		a.newStatusCommand(),
		a.newMCPCommand(),
		a.newConfigCommand(),
		a.newVersionCommand(),
	)
	return cmd
}

func (a *app) close() error {
	var err error
	for i := len(a.closers) - 1; i >= 0; i-- {
		err = errors.Join(err, a.closers[i]())
	}
	a.closers = nil
	return err
}
