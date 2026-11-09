package cli

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/mcpserver"
	"github.com/bengearig/ralph/internal/version"
)

func (a *app) newMCPCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve the project's store to an agent harness over MCP (stdio)",
		Long: `Runs an MCP server on stdin/stdout for the project in the current directory.
Agent harnesses launch it themselves; ` + "`ralph init`" + ` registers it with Claude Code.
Logs go to stderr because stdout carries the protocol.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			l, err := a.loadConfig()
			if err != nil {
				return err
			}
			srv := mcpserver.New(svc, version.Get().Version, a.logger(), mcpserver.Options{ConfirmOutline: l.Config.Receive.ConfirmOutline})
			return srv.Run(cmd.Context(), &mcp.StdioTransport{})
		},
	}
}
