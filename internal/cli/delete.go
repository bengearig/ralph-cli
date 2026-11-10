package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/service"
)

const cancelReminder = `

To drop work you may want back, prefer "ralph task status <id> CANCELLED";
delete is for mistakes.`

// newDeleteCommand builds "<kind> delete <id>"; withForce adds --force for
// kinds whose delete can cascade or discard progressed work.
func (a *app) newDeleteCommand(kind domain.Kind, withForce bool, long string) *cobra.Command {
	var yes, force, dryRun bool
	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a " + string(kind) + " permanently",
		Long:  long + cancelReminder,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseIDArg(kind, args[0])
			if err != nil {
				return err
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			preview, err := svc.PreviewDelete(cmd.Context(), kind, id, force)
			if err != nil {
				return deleteRefusal(err, withForce)
			}
			if dryRun {
				if a.jsonOut {
					return a.writeJSON(preview)
				}
				return a.printf("would delete %s%s\n%s", preview.ID, countSuffix(preview), a.cardsLine(svc, preview))
			}
			if !yes {
				ok, err := a.confirmDelete(svc, preview)
				if err != nil {
					return err
				}
				if !ok {
					return a.printf("cancelled\n")
				}
			}
			var v service.DeleteView
			switch kind {
			case domain.KindInitiative:
				v, err = svc.DeleteInitiative(cmd.Context(), id, force)
			case domain.KindMilestone:
				v, err = svc.DeleteMilestone(cmd.Context(), id, force)
			case domain.KindTask:
				v, err = svc.DeleteTask(cmd.Context(), id, force)
			case domain.KindDocument:
				v, err = svc.DeleteDocument(cmd.Context(), id)
			case domain.KindNote:
				v, err = svc.DeleteNote(cmd.Context(), id)
			}
			if err != nil {
				return deleteRefusal(err, withForce)
			}
			for _, c := range v.Cards {
				if !c.Moved {
					fmt.Fprintf(a.streams.Err, "warning: card %s was not moved: %s\n", c.Key, c.Error)
				}
			}
			if a.jsonOut {
				return a.writeJSON(v)
			}
			return a.printf("deleted %s%s\n", v.ID, countSuffix(v))
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be deleted without deleting")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	if withForce {
		cmd.Flags().BoolVar(&force, "force", false, "delete even when it removes more than the target or discards progressed work")
	}
	return cmd
}

func deleteRefusal(err error, withForce bool) error {
	if withForce && errors.Is(err, service.ErrNeedsForce) {
		return fmt.Errorf("%w (use --force)", err)
	}
	return err
}

// confirmDelete asks on stderr; only y or yes proceeds. Without a terminal it
// refuses rather than guessing.
func (a *app) confirmDelete(svc *service.Service, v service.DeleteView) (bool, error) {
	if !a.streams.IsTerminal {
		return false, errors.New("refusing to delete without confirmation: stdin is not a terminal; pass --yes")
	}
	prompt := "Delete " + v.ID
	if c := countList(v); c != "" {
		prompt += " with " + c
	}
	fmt.Fprint(a.streams.Err, a.cardsLine(svc, v)+prompt+"? This cannot be undone. [y/N] ")
	line, err := bufio.NewReader(a.streams.In).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

// cardsLine names the cards a delete will move to the canceled status, with a trailing newline; empty when none.
func (a *app) cardsLine(svc *service.Service, v service.DeleteView) string {
	status := svc.CardCanceledStatus()
	if len(v.Cards) == 0 || status == "" {
		return ""
	}
	keys := make([]string, len(v.Cards))
	for i, c := range v.Cards {
		keys[i] = c.Key
	}
	return fmt.Sprintf("cards: %s will move to %q\n", strings.Join(keys, ", "), status)
}

// countParts lists the non-zero counts other than the target itself, in
// hierarchy order.
func countParts(v service.DeleteView) []string {
	own := map[string]string{"initiative": "initiatives", "milestone": "milestones", "task": "tasks", "note": "notes", "document": "documents"}[v.Kind]
	var parts []string
	for _, k := range []string{"initiatives", "milestones", "tasks", "notes", "documents"} {
		n := v.Deleted[k]
		if n == 0 || (k == own && n == 1) {
			continue
		}
		noun := k
		if n == 1 {
			noun = strings.TrimSuffix(k, "s")
		}
		parts = append(parts, fmt.Sprintf("%d %s", n, noun))
	}
	return parts
}

func countList(v service.DeleteView) string {
	parts := countParts(v)
	if len(parts) < 2 {
		return strings.Join(parts, "")
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

func countSuffix(v service.DeleteView) string {
	if parts := countParts(v); len(parts) > 0 {
		return " (" + strings.Join(parts, ", ") + ")"
	}
	return ""
}

func (a *app) newNoteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "note",
		Short: "Manage task notes",
	}
	cmd.AddCommand(a.newDeleteCommand(domain.KindNote, false, "Delete a task note by its N-n ID."))
	return cmd
}
