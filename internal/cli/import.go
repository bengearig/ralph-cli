package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/importer"
	"github.com/bengearig/ralph/internal/service"
)

type importResult struct {
	Queued   []service.ImportOutcome `json:"queued"`
	Skipped  []importer.Skipped      `json:"skipped"`
	Warnings []string                `json:"warnings"`
}

func (a *app) newImportCommand() *cobra.Command {
	var dirs, files []string
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Queue files and directories for import as documents",
		Long: `Queues text files for integration into ralph's documents. In a git work tree,
a directory contributes its tracked and untracked files minus ignored ones.
Files that are too large (import.max_file_bytes), not UTF-8 text, symlinks, or
outside the project are skipped with a reason. Skipped files do not change the
exit status.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(dirs)+len(files) == 0 {
				return errors.New("give at least one --dir or --file")
			}
			p, err := a.findProject()
			if err != nil {
				return err
			}
			l, err := a.loadConfig()
			if err != nil {
				return err
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			inputs := make([]importer.Input, 0, len(dirs)+len(files))
			for _, d := range dirs {
				inputs = append(inputs, importer.Input{Path: d, Dir: true})
			}
			for _, f := range files {
				inputs = append(inputs, importer.Input{Path: f})
			}
			collected, skipped, warnings, err := importer.Collect(cmd.Context(), p.Root, inputs, l.Config.Import.MaxFileBytes)
			if err != nil {
				return err
			}
			outcomes, err := svc.QueueImports(cmd.Context(), collected)
			if err != nil {
				return err
			}
			pending, err := svc.ListImports(cmd.Context(), []domain.ImportStatus{domain.ImportPending})
			if err != nil {
				return err
			}
			if a.jsonOut {
				if warnings == nil {
					warnings = []string{}
				}
				return a.writeJSON(importResult{Queued: outcomes, Skipped: skipped, Warnings: warnings})
			}
			for _, w := range warnings {
				a.warn(w)
			}
			return a.printImport(outcomes, skipped, len(pending))
		},
	}
	cmd.Flags().StringArrayVar(&dirs, "dir", nil, "directory to import (repeatable)")
	cmd.Flags().StringArrayVar(&files, "file", nil, "file to import (repeatable)")
	return cmd
}

func (a *app) printImport(outcomes []service.ImportOutcome, skipped []importer.Skipped, pending int) error {
	groups := []struct{ outcome, label string }{
		{service.OutcomeNew, "new"},
		{service.OutcomeChanged, "changed"},
		{service.OutcomeUnchanged, "unchanged"},
		{service.OutcomeQueued, "already queued"},
	}
	printed := false
	heading := func(label string, n int) error {
		if printed {
			if err := a.printf("\n"); err != nil {
				return err
			}
		}
		printed = true
		return a.printf("%s (%d):\n", label, n)
	}
	for _, g := range groups {
		var paths []string
		for _, o := range outcomes {
			if o.Outcome == g.outcome {
				paths = append(paths, o.Path)
			}
		}
		if len(paths) == 0 {
			continue
		}
		if err := heading(g.label, len(paths)); err != nil {
			return err
		}
		for _, p := range paths {
			if err := a.printf("  %s\n", p); err != nil {
				return err
			}
		}
	}
	if len(skipped) > 0 {
		if err := heading("skipped", len(skipped)); err != nil {
			return err
		}
		for _, s := range skipped {
			if err := a.printf("  %s: %s\n", s.Path, s.Reason); err != nil {
				return err
			}
		}
	}
	if !printed {
		if err := a.printf("nothing to import\n"); err != nil {
			return err
		}
	}
	if pending > 0 {
		return a.printf("\nnext: run /ralph:import (Claude Code) or the ralph MCP import prompt to integrate %d queued file(s)\n", pending)
	}
	return nil
}
