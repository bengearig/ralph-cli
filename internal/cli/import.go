package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/errs"
	"github.com/bengearig/ralph/internal/importer"
	"github.com/bengearig/ralph/internal/service"
)

type asNewResult struct {
	Imported []service.AsNewOutcome `json:"imported"`
	Skipped  []importer.Skipped     `json:"skipped"`
	Warnings []string               `json:"warnings"`
}

type importResult struct {
	Queued   []service.ImportOutcome `json:"queued"`
	Skipped  []importer.Skipped      `json:"skipped"`
	Warnings []string                `json:"warnings"`
}

func (a *app) newImportCommand() *cobra.Command {
	var dirs, files []string
	var asNew, again bool
	var kind, initiative string
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
			if !asNew && (again || cmd.Flags().Changed("kind") || initiative != "") {
				return errors.New("--kind, --initiative and --again require --as-new")
			}
			var opts service.AsNewOptions
			if asNew {
				k, err := domain.ParseDocKind(kind)
				if err != nil {
					return err
				}
				iID, err := parseIDFlag(domain.KindInitiative, initiative)
				if err != nil {
					return err
				}
				opts = service.AsNewOptions{Kind: k, InitiativeID: iID, Again: again}
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
			if asNew {
				return a.runAsNew(cmd, svc, collected, skipped, warnings, opts)
			}
			outcomes, err := svc.QueueImports(cmd.Context(), collected, nil)
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
	cmd.Flags().BoolVar(&asNew, "as-new", false, "import each file as one new document and mark it done")
	cmd.Flags().StringVar(&kind, "kind", string(domain.DocReference), "with --as-new: document kind")
	cmd.Flags().StringVar(&initiative, "initiative", "", "with --as-new: scope documents to an initiative (default: project-wide)")
	cmd.Flags().BoolVar(&again, "again", false, "with --as-new: create a new document even for files already imported")
	return cmd
}

func (a *app) runAsNew(cmd *cobra.Command, svc *service.Service, files []service.ImportFile, skipped []importer.Skipped, warnings []string, opts service.AsNewOptions) error {
	outcomes, err := svc.ImportAsNew(cmd.Context(), files, opts)
	if err != nil {
		return err
	}
	var refused, failed int
	for _, o := range outcomes {
		switch o.Outcome {
		case service.AsNewRefused:
			refused++
		case service.AsNewFailed:
			failed++
		}
	}
	if a.jsonOut {
		if warnings == nil {
			warnings = []string{}
		}
		if skipped == nil {
			skipped = []importer.Skipped{}
		}
		if err := a.writeJSON(asNewResult{Imported: outcomes, Skipped: skipped, Warnings: warnings}); err != nil {
			return err
		}
	} else {
		for _, w := range warnings {
			a.warn(w)
		}
		if err := a.printAsNew(outcomes, skipped); err != nil {
			return err
		}
	}
	if refused+failed > 0 {
		return fmt.Errorf("%w: %d file(s) refused and %d failed", errs.ErrNeedsHuman, refused, failed)
	}
	return nil
}

func (a *app) printAsNew(outcomes []service.AsNewOutcome, skipped []importer.Skipped) error {
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
	for _, g := range []string{service.AsNewCreated, service.AsNewSkipped, service.AsNewRefused, service.AsNewFailed} {
		var group []service.AsNewOutcome
		for _, o := range outcomes {
			if o.Outcome == g {
				group = append(group, o)
			}
		}
		if len(group) == 0 {
			continue
		}
		if err := heading(g, len(group)); err != nil {
			return err
		}
		for _, o := range group {
			line := o.Path
			if o.Document != "" {
				line += " -> " + o.Document
			} else if o.Reason != "" {
				line += ": " + o.Reason
			}
			if err := a.printf("  %s\n", line); err != nil {
				return err
			}
		}
	}
	if len(skipped) > 0 {
		if err := heading("not imported", len(skipped)); err != nil {
			return err
		}
		for _, s := range skipped {
			if err := a.printf("  %s: %s\n", s.Path, s.Reason); err != nil {
				return err
			}
		}
	}
	if !printed {
		return a.printf("nothing to import\n")
	}
	return nil
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
