package domain

import (
	"fmt"
	"strings"
	"time"
)

// ImportStatus is where an import record stands in the import queue.
type ImportStatus string

const (
	ImportPending  ImportStatus = "pending"
	ImportDone     ImportStatus = "done"
	ImportSkipped  ImportStatus = "skipped"
	ImportDeferred ImportStatus = "deferred"
)

// ImportStatuses returns every import status.
func ImportStatuses() []ImportStatus {
	return []ImportStatus{ImportPending, ImportDone, ImportSkipped, ImportDeferred}
}

// ParseImportStatus accepts an import status case-insensitively.
func ParseImportStatus(s string) (ImportStatus, error) {
	st := ImportStatus(strings.ToLower(strings.TrimSpace(s)))
	names := make([]string, 0, len(ImportStatuses()))
	for _, known := range ImportStatuses() {
		if st == known {
			return st, nil
		}
		names = append(names, string(known))
	}
	return "", fmt.Errorf("%w: unknown import status %q (expected one of %s)", ErrInvalid, s, strings.Join(names, ", "))
}

// ImportRelation says how an import relates to a document it produced.
type ImportRelation string

const (
	ImportCreated  ImportRelation = "created"
	ImportUpdated  ImportRelation = "updated"
	ImportDecision ImportRelation = "decision"
)

// ImportRelations returns every import relation.
func ImportRelations() []ImportRelation {
	return []ImportRelation{ImportCreated, ImportUpdated, ImportDecision}
}

// Import records a file queued for import, addressed by its project-relative
// Path. Zero times and empty strings are stored as empty text.
type Import struct {
	ID           int64
	Path         string
	Status       ImportStatus
	Content      string
	ContentHash  string
	Size         int64
	LastDoneHash string
	Note         string
	QueuedAt     time.Time
	ProcessedAt  time.Time
}

// ImportLink ties an import to a document it produced.
type ImportLink struct {
	DocumentID int64
	Relation   ImportRelation
	CreatedAt  time.Time
}
