package cli

import (
	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/service"
)

func (a *app) newCardsIDCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "id",
		Short: "Show the store identifier stamped on cards",
		Long: `Shows the store identifier and where it comes from.

The identifier tells this clone's cards apart from other clones' cards in a
shared task manager project. It appears in the card summary, for example
"[ab] T-12: title", and as a ralph-store-<id> label.

It is per clone. Set it with task_manager.store_id in the global config or with
RALPH_TASK_MANAGER__STORE_ID, not in the committed project file. It is 2 to 6
lowercase letters or digits. Without a setting, a 3 character identifier is
generated when the first card is created and kept in the store.

This command writes nothing and works with the task manager disabled.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			id, source, err := svc.StoreID(cmd.Context())
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.writeJSON(struct {
					StoreID string                `json:"store_id"`
					Source  service.StoreIDSource `json:"source"`
				}{id, source})
			}
			switch source {
			case service.StoreIDConfig:
				return a.printf("%s (configured)\n", id)
			case service.StoreIDStore:
				return a.printf("%s (generated, kept in the store)\n", id)
			}
			return a.printf("none yet; one is generated when the first card is created\n")
		},
	}
}
