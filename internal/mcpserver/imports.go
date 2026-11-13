package mcpserver

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/importer"
	"github.com/bengearig/ralph/internal/service"
)

const importWorkflow = "Import workflow: import_queue the files, then for each pending or deferred record in import_list " +
	"call import_get, integrate its content into ralph's documents (create or update them with doc_create and doc_update), " +
	"and finish with import_resolve, linking every document created or updated. " +
	"After import, ralph's documents are the source of truth, not the imported files. " +
	"For a thorough import, follow the import MCP prompt (/ralph:import in Claude Code). "

type importQueueIn struct {
	Paths        []string `json:"paths" jsonschema:"project-relative files or directories to queue"`
	AsNew        bool     `json:"as_new,omitempty" jsonschema:"fast path: import each file verbatim as one new document and mark it done, with no integration"`
	Kind         string   `json:"kind,omitempty" jsonschema:"with as_new: document kind; defaults to reference"`
	InitiativeID string   `json:"initiative_id,omitempty" jsonschema:"with as_new: scope documents to this initiative; omit for project-wide"`
	Again        bool     `json:"again,omitempty" jsonschema:"with as_new: create a new document even for files that were already imported"`
}

type importQueueOut struct {
	Queued   []service.ImportOutcome `json:"queued,omitempty" jsonschema:"per-file queue outcome: new, changed, unchanged, or queued"`
	Imported []service.AsNewOutcome  `json:"imported,omitempty" jsonschema:"with as_new: per-file outcome with the created document ID"`
	Skipped  []importer.Skipped      `json:"skipped" jsonschema:"inputs and files not queued, with reasons"`
	Warnings []string                `json:"warnings"`
}

func (t *tools) importQueue(ctx context.Context, _ *mcp.CallToolRequest, in importQueueIn) (*mcp.CallToolResult, importQueueOut, error) {
	var opts service.AsNewOptions
	if in.AsNew {
		k := domain.DocReference
		if in.Kind != "" {
			var err error
			if k, err = domain.ParseDocKind(in.Kind); err != nil {
				return nil, importQueueOut{}, err
			}
		}
		iID, err := parseOptionalID(domain.KindInitiative, in.InitiativeID)
		if err != nil {
			return nil, importQueueOut{}, err
		}
		opts = service.AsNewOptions{Kind: k, InitiativeID: iID, Again: in.Again}
	} else if in.Kind != "" || in.InitiativeID != "" || in.Again {
		return nil, importQueueOut{}, errors.New("kind, initiative_id and again require as_new")
	}

	inputs := make([]importer.Input, 0, len(in.Paths))
	given := map[string]string{}
	for _, p := range in.Paths {
		abs := p
		if !filepath.IsAbs(p) {
			abs = filepath.Join(t.projectRoot, filepath.FromSlash(p))
		}
		info, err := os.Stat(abs)
		inputs = append(inputs, importer.Input{Path: abs, Dir: err == nil && info.IsDir()})
		given[filepath.ToSlash(abs)] = p
	}
	files, skipped, warnings, err := importer.Collect(ctx, t.projectRoot, inputs, t.importMaxBytes)
	if err != nil {
		return nil, importQueueOut{}, toolError(err)
	}
	for i, s := range skipped {
		if p, ok := given[s.Path]; ok {
			skipped[i].Path = p
		}
	}
	if warnings == nil {
		warnings = []string{}
	}
	out := importQueueOut{Skipped: skipped, Warnings: warnings}
	if in.AsNew {
		out.Imported, err = t.svc.ImportAsNew(ctx, files, opts)
	} else {
		out.Queued, err = t.svc.QueueImports(ctx, files, nil)
	}
	return nil, out, toolError(err)
}

type importListIn struct {
	Statuses []string `json:"statuses,omitempty" jsonschema:"only records in these statuses: pending, done, skipped, deferred; omit for all"`
}

type importList struct {
	Imports []service.ImportView `json:"imports"`
}

func (t *tools) importList(ctx context.Context, _ *mcp.CallToolRequest, in importListIn) (*mcp.CallToolResult, importList, error) {
	statuses := make([]domain.ImportStatus, 0, len(in.Statuses))
	for _, s := range in.Statuses {
		st, err := domain.ParseImportStatus(s)
		if err != nil {
			return nil, importList{}, err
		}
		statuses = append(statuses, st)
	}
	v, err := t.svc.ListImports(ctx, statuses)
	if v == nil {
		v = []service.ImportView{}
	}
	return nil, importList{Imports: v}, toolError(err)
}

type importGetIn struct {
	Path string `json:"path" jsonschema:"project-relative, slash-separated path of the import record"`
}

func (t *tools) importGet(ctx context.Context, _ *mcp.CallToolRequest, in importGetIn) (*mcp.CallToolResult, service.ImportView, error) {
	v, err := t.svc.GetImport(ctx, in.Path)
	return nil, v, toolError(err)
}

type importLinkIn struct {
	DocumentID string `json:"document_id" jsonschema:"document ID, for example D-4"`
	Relation   string `json:"relation" jsonschema:"one of created, updated, decision"`
}

type importResolveIn struct {
	Path   string         `json:"path" jsonschema:"project-relative path of the import record"`
	Status string         `json:"status" jsonschema:"done, skipped, or deferred"`
	Note   string         `json:"note,omitempty" jsonschema:"required for skipped (the reason) and deferred (the open contradictions)"`
	Links  []importLinkIn `json:"links,omitempty" jsonschema:"documents this import created, updated, or produced as decisions"`
}

func (t *tools) importResolve(ctx context.Context, _ *mcp.CallToolRequest, in importResolveIn) (*mcp.CallToolResult, service.ImportView, error) {
	st, err := domain.ParseImportStatus(in.Status)
	if err != nil {
		return nil, service.ImportView{}, err
	}
	links := make([]service.ImportLinkInput, len(in.Links))
	for i, l := range in.Links {
		links[i] = service.ImportLinkInput{Document: l.DocumentID, Relation: domain.ImportRelation(l.Relation)}
	}
	v, err := t.svc.ResolveImport(ctx, in.Path, st, in.Note, links)
	return nil, v, toolError(err)
}

func (t *tools) registerImports(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "import_queue", Annotations: write("Queue imports"),
		Description: importWorkflow + "import_queue is the first step: it queues project-relative files or directories, " +
			"skipping (not failing on) paths that are missing, outside the project, too large, or not text; unchanged files already imported are left alone. " +
			"With as_new it is the fast path: each file becomes one new document verbatim and is marked done, with no integration, so skip import_get and import_resolve."}, t.importQueue)
	mcp.AddTool(s, &mcp.Tool{Name: "import_list", Annotations: readOnly("List imports"),
		Description: importWorkflow + "import_list shows records without content; filter by statuses (pending and deferred still need work)."}, t.importList)
	mcp.AddTool(s, &mcp.Tool{Name: "import_get", Annotations: readOnly("Get import"),
		Description: importWorkflow + "import_get returns one record with its content snapshot and linked documents; read it, then integrate it into documents."}, t.importGet)
	mcp.AddTool(s, &mcp.Tool{Name: "import_resolve", Annotations: write("Resolve import"),
		Description: importWorkflow + "import_resolve is the last step for a pending or deferred record: status done, skipped, or deferred, with links to the documents created, updated, or decided. " +
			"skipped and deferred require a note; for deferred, list the open contradictions in note."}, t.importResolve)
}
