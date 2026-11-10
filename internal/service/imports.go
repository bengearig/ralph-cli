package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/store"
)

// ImportFile is a file to queue, with its project-relative path.
type ImportFile struct {
	Path    string
	Content string
}

// ImportLinkInput links an import to a document by display ID (D-n).
type ImportLinkInput struct {
	Document string
	Relation domain.ImportRelation
}

// Outcomes of queueing a file.
const (
	OutcomeNew       = "new"
	OutcomeChanged   = "changed"
	OutcomeUnchanged = "unchanged"
	OutcomeQueued    = "queued"
)

// ImportOutcome says what queueing did for one path.
type ImportOutcome struct {
	Path    string `json:"path"`
	Outcome string `json:"outcome"`
}

// ValidateImportPath requires a clean, project-relative, slash-separated path.
func ValidateImportPath(p string) error {
	if p == "" || strings.Contains(p, `\`) || strings.ContainsRune(p, 0) || strings.HasPrefix(p, "/") ||
		path.Clean(p) != p || p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return fmt.Errorf("%w: import path %q must be clean, project-relative and slash-separated", domain.ErrInvalid, p)
	}
	return nil
}

func hashContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// QueueImports queues files in one transaction, applying change detection
// against any existing record for each path.
func (s *Service) QueueImports(ctx context.Context, files []ImportFile) ([]ImportOutcome, error) {
	for _, f := range files {
		if err := ValidateImportPath(f.Path); err != nil {
			return nil, err
		}
	}
	out := make([]ImportOutcome, 0, len(files))
	err := s.InTx(ctx, func(tx *Service) error {
		out = out[:0]
		for _, f := range files {
			o, err := tx.queueImport(ctx, f)
			if err != nil {
				return err
			}
			out = append(out, ImportOutcome{Path: f.Path, Outcome: o})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) queueImport(ctx context.Context, f ImportFile) (string, error) {
	hash := hashContent(f.Content)
	now := time.Now().UTC()
	im, err := s.store.GetImportByPath(ctx, f.Path)
	if errors.Is(err, store.ErrNotFound) {
		nw := domain.Import{Path: f.Path, Status: domain.ImportPending, Content: f.Content, ContentHash: hash,
			Size: int64(len(f.Content)), QueuedAt: now}
		return OutcomeNew, s.store.CreateImport(ctx, &nw)
	}
	if err != nil {
		return "", err
	}
	if im.ContentHash == hash {
		if im.Status == domain.ImportDone || im.Status == domain.ImportSkipped {
			return OutcomeUnchanged, nil
		}
		return OutcomeQueued, nil
	}
	im.Content, im.ContentHash, im.Size = f.Content, hash, int64(len(f.Content))
	im.QueuedAt, im.Status, im.Note, im.ProcessedAt = now, domain.ImportPending, "", time.Time{}
	return OutcomeChanged, s.store.UpdateImport(ctx, &im)
}

// ListImports omits content. No statuses means all.
func (s *Service) ListImports(ctx context.Context, statuses []domain.ImportStatus) ([]ImportView, error) {
	ims, err := s.store.ListImports(ctx, statuses)
	if err != nil {
		return nil, err
	}
	out := make([]ImportView, 0, len(ims))
	for _, im := range ims {
		links, err := s.importLinkViews(ctx, im.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, importView(im, links, false))
	}
	return out, nil
}

// GetImport includes the content snapshot.
func (s *Service) GetImport(ctx context.Context, p string) (ImportView, error) {
	im, err := s.store.GetImportByPath(ctx, p)
	if err != nil {
		return ImportView{}, err
	}
	return s.importViewOf(ctx, im, true)
}

func (s *Service) importViewOf(ctx context.Context, im domain.Import, withContent bool) (ImportView, error) {
	links, err := s.importLinkViews(ctx, im.ID)
	if err != nil {
		return ImportView{}, err
	}
	return importView(im, links, withContent), nil
}

func (s *Service) importLinkViews(ctx context.Context, importID int64) ([]ImportLinkView, error) {
	links, err := s.store.ListImportLinks(ctx, importID)
	if err != nil {
		return nil, err
	}
	out := make([]ImportLinkView, 0, len(links))
	for _, l := range links {
		out = append(out, ImportLinkView{Document: domain.FormatID(domain.KindDocument, l.DocumentID), Relation: string(l.Relation)})
	}
	return out, nil
}

// ResolveImport moves a pending or deferred import to done, skipped or
// deferred, optionally linking the documents it produced.
func (s *Service) ResolveImport(ctx context.Context, p string, status domain.ImportStatus, note string, links []ImportLinkInput) (ImportView, error) {
	note = strings.TrimSpace(note)
	switch status {
	case domain.ImportDone:
	case domain.ImportSkipped, domain.ImportDeferred:
		if note == "" {
			return ImportView{}, fmt.Errorf("%w: a note is required to mark an import %s", domain.ErrInvalid, status)
		}
	default:
		return ImportView{}, fmt.Errorf("%w: an import can be resolved as done, skipped or deferred, not %q", domain.ErrInvalid, status)
	}
	docIDs := make([]int64, len(links))
	for i, l := range links {
		id, err := domain.ParseID(domain.KindDocument, l.Document)
		if err != nil {
			return ImportView{}, err
		}
		if !validImportRelation(l.Relation) {
			return ImportView{}, fmt.Errorf("%w: unknown import relation %q", domain.ErrInvalid, l.Relation)
		}
		docIDs[i] = id
	}
	var view ImportView
	err := s.InTx(ctx, func(tx *Service) error {
		im, err := tx.store.GetImportByPath(ctx, p)
		if err != nil {
			return err
		}
		if im.Status != domain.ImportPending && im.Status != domain.ImportDeferred {
			return fmt.Errorf("%w: import %s is %s; only pending or deferred imports can be resolved", domain.ErrInvalid, p, im.Status)
		}
		for i, l := range links {
			if _, err := tx.store.GetDocument(ctx, docIDs[i]); err != nil {
				return err
			}
			if err := tx.store.AddImportLink(ctx, im.ID, docIDs[i], l.Relation); err != nil {
				return err
			}
		}
		im.Status, im.Note = status, note
		if status == domain.ImportDone {
			im.LastDoneHash, im.ProcessedAt = im.ContentHash, time.Now().UTC()
		}
		if err := tx.store.UpdateImport(ctx, &im); err != nil {
			return err
		}
		view, err = tx.importViewOf(ctx, im, true)
		return err
	})
	return view, err
}

func validImportRelation(r domain.ImportRelation) bool {
	for _, known := range domain.ImportRelations() {
		if r == known {
			return true
		}
	}
	return false
}
