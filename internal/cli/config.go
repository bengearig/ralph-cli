package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/config"
	"github.com/bengearig/ralph/internal/service"
)

func (a *app) newConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect the effective configuration",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "show",
			Short: "Print the effective merged configuration",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				l, err := a.loadConfig()
				if err != nil {
					return err
				}
				if a.jsonOut {
					return a.writeJSON(l.Config)
				}
				out, err := l.YAML()
				if err != nil {
					return err
				}
				_, err = a.streams.Out.Write(out)
				return err
			},
		},
		&cobra.Command{
			Use:   "path",
			Short: "List each configuration layer and whether it is present",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				l, err := a.loadConfig()
				if err != nil {
					return err
				}
				if a.jsonOut {
					return a.writeJSON(l.Layers)
				}
				rows := make([][]string, 0, len(l.Layers))
				for _, layer := range l.Layers {
					present := "no"
					if layer.Present {
						present = "yes"
					}
					path := layer.Path
					if path == "" {
						path = "-"
					}
					rows = append(rows, []string{layer.Name, present, path})
				}
				return a.writeTable([]string{"LAYER", "PRESENT", "PATH"}, rows)
			},
		},
		&cobra.Command{
			Use:   "validate",
			Short: "Load and validate the configuration",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				l, err := a.loadConfig()
				if err != nil {
					return err
				}
				if _, err := service.NewTaskManager(l.Config); err != nil {
					fmt.Fprintf(a.streams.Err, "task manager disabled: %v\n", err)
				}
				return a.printf("configuration is valid\n")
			},
		},
		&cobra.Command{
			Use:   "steps",
			Short: "Show the effective harness and model for each step",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				l, err := a.loadConfig()
				if err != nil {
					return err
				}
				var res []config.StepResolution
				for _, s := range config.Steps() {
					r, err := l.Config.ResolveStep(s)
					if err != nil {
						return err
					}
					res = append(res, r)
				}
				if a.jsonOut {
					return a.writeJSON(res)
				}
				rows := make([][]string, 0, len(res))
				for _, r := range res {
					model := r.Model
					if model == "" {
						model = "(harness default)"
					}
					rows = append(rows, []string{string(r.Step), r.Harness, model})
				}
				return a.writeTable([]string{"STEP", "HARNESS", "MODEL"}, rows)
			},
		},
	)
	return cmd
}
