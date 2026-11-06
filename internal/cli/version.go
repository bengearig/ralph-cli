package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/version"
)

func (a *app) newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the ralph version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := version.Get()
			if a.jsonOut {
				return a.writeJSON(info)
			}
			_, err := fmt.Fprintf(a.streams.Out, "ralph %s\n", info)
			return err
		},
	}
}
