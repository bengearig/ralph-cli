package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/service"
)

func docKindNames() string {
	names := make([]string, 0, len(domain.DocKinds()))
	for _, k := range domain.DocKinds() {
		names = append(names, string(k))
	}
	return strings.Join(names, ", ")
}

func (a *app) newDocCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doc",
		Short: "Manage architecture, design, and other documents",
	}

	var initiative, kind, title, slug, body, file string
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a document",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, err := domain.ParseDocKind(kind)
			if err != nil {
				return err
			}
			iID, err := parseIDFlag(domain.KindInitiative, initiative)
			if err != nil {
				return err
			}
			b, err := a.textFlag(cmd, "body", body, "file", file)
			if err != nil {
				return err
			}
			in := service.DocumentInput{InitiativeID: iID, Kind: k, Title: title, Slug: slug}
			if b != nil {
				in.Body = *b
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			v, err := svc.CreateDocument(cmd.Context(), in)
			if err != nil {
				return err
			}
			a.warn(v.IndexWarning)
			if a.jsonOut {
				return a.writeJSON(v)
			}
			return a.printf("created %s %s\n", v.ID, v.Slug)
		},
	}
	create.Flags().StringVar(&initiative, "initiative", "", "scope to an initiative (default: project-wide)")
	create.Flags().StringVar(&kind, "kind", "", "document kind: "+docKindNames()+" (required)")
	create.Flags().StringVar(&title, "title", "", "title (required)")
	create.Flags().StringVar(&slug, "slug", "", "slug (default: derived from title)")
	create.Flags().StringVar(&body, "body", "", "markdown body")
	create.Flags().StringVar(&file, "file", "", "read the body from a file, or - for stdin")
	_ = create.MarkFlagRequired("kind")
	_ = create.MarkFlagRequired("title")

	var listInitiative, listKind string
	var projectOnly bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List documents",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if projectOnly && listInitiative != "" {
				return fmt.Errorf("use either --project or --initiative, not both")
			}
			iID, err := parseIDFlag(domain.KindInitiative, listInitiative)
			if err != nil {
				return err
			}
			f := service.DocumentFilter{InitiativeID: iID, ProjectOnly: projectOnly}
			if listKind != "" {
				if f.Kind, err = domain.ParseDocKind(listKind); err != nil {
					return err
				}
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			docs, err := svc.ListDocuments(cmd.Context(), f)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.writeJSON(docs)
			}
			rows := make([][]string, 0, len(docs))
			for _, d := range docs {
				rows = append(rows, []string{d.ID, orDash(d.InitiativeID), d.Kind, d.Slug, d.Title})
			}
			return a.writeTable([]string{"ID", "INITIATIVE", "KIND", "SLUG", "TITLE"}, rows)
		},
	}
	list.Flags().StringVar(&listInitiative, "initiative", "", "only this initiative's documents")
	list.Flags().BoolVar(&projectOnly, "project", false, "only project-wide documents")
	list.Flags().StringVar(&listKind, "kind", "", "only this kind")

	show := &cobra.Command{
		Use:   "show <id>",
		Short: "Print a document",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseIDArg(domain.KindDocument, args[0])
			if err != nil {
				return err
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			v, err := svc.GetDocument(cmd.Context(), id)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.writeJSON(v)
			}
			if err := a.printf("%s  %s\nkind: %s\nslug: %s\ninitiative: %s\n\n", v.ID, v.Title, v.Kind, v.Slug, orDash(v.InitiativeID)); err != nil {
				return err
			}
			return a.printf("%s\n", v.Body)
		},
	}

	var upKind, upTitle, upBody, upFile string
	update := &cobra.Command{
		Use:   "update <id>",
		Short: "Edit a document",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseIDArg(domain.KindDocument, args[0])
			if err != nil {
				return err
			}
			b, err := a.textFlag(cmd, "body", upBody, "file", upFile)
			if err != nil {
				return err
			}
			p := service.DocumentPatch{Title: optString(cmd, "title", upTitle), Body: b}
			if cmd.Flags().Changed("kind") {
				k, err := domain.ParseDocKind(upKind)
				if err != nil {
					return err
				}
				p.Kind = &k
			}
			svc, err := a.service(cmd.Context())
			if err != nil {
				return err
			}
			v, err := svc.UpdateDocument(cmd.Context(), id, p)
			if err != nil {
				return err
			}
			a.warn(v.IndexWarning)
			if a.jsonOut {
				return a.writeJSON(v)
			}
			return a.printf("updated %s\n", v.ID)
		},
	}
	update.Flags().StringVar(&upKind, "kind", "", "new kind")
	update.Flags().StringVar(&upTitle, "title", "", "new title")
	update.Flags().StringVar(&upBody, "body", "", "new body")
	update.Flags().StringVar(&upFile, "file", "", "read the new body from a file, or - for stdin")

	cmd.AddCommand(create, list, show, update, a.newDeleteCommand(domain.KindDocument, false, "Delete a document and its search index entries."))
	return cmd
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
