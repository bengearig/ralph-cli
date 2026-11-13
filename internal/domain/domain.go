// Package domain defines ralph's core entities and the rules governing them.
package domain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var (
	// ErrInvalid marks input that violates an entity's invariants.
	ErrInvalid = errors.New("invalid input")
	// ErrInvalidTransition marks a status change the state machine forbids.
	ErrInvalidTransition = errors.New("invalid status transition")
)

// Kind identifies an entity type. Its prefix forms display IDs such as T-12.
type Kind string

const (
	KindInitiative Kind = "initiative"
	KindMilestone  Kind = "milestone"
	KindTask       Kind = "task"
	KindDocument   Kind = "document"
	KindNote       Kind = "note"
)

// Kinds returns the searchable entity kinds; KindNote is not among them.
func Kinds() []Kind { return []Kind{KindInitiative, KindMilestone, KindTask, KindDocument} }

// Prefix is the display-ID prefix for the kind.
func (k Kind) Prefix() string {
	switch k {
	case KindInitiative:
		return "I"
	case KindMilestone:
		return "M"
	case KindTask:
		return "T"
	case KindDocument:
		return "D"
	case KindNote:
		return "N"
	}
	return "?"
}

// ParseKind accepts a kind name case-insensitively.
func ParseKind(s string) (Kind, error) {
	k := Kind(strings.ToLower(strings.TrimSpace(s)))
	for _, known := range Kinds() {
		if k == known {
			return k, nil
		}
	}
	return "", fmt.Errorf("%w: unknown kind %q", ErrInvalid, s)
}

// FormatID renders a display ID such as M-3.
func FormatID(k Kind, id int64) string {
	return k.Prefix() + "-" + strconv.FormatInt(id, 10)
}

// ParseID accepts a display ID ("T-12", case-insensitive) or a bare number.
func ParseID(k Kind, s string) (int64, error) {
	raw := strings.TrimSpace(s)
	if p, rest, ok := strings.Cut(raw, "-"); ok {
		if !strings.EqualFold(p, k.Prefix()) {
			return 0, fmt.Errorf("%w: %q is not a %s ID (expected %s-<n>)", ErrInvalid, s, k, k.Prefix())
		}
		raw = rest
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 {
		return 0, fmt.Errorf("%w: %q is not a %s ID (expected %s-<n>)", ErrInvalid, s, k, k.Prefix())
	}
	return id, nil
}

// Initiative is one set of received instructions and everything articulated
// from it.
type Initiative struct {
	ID    int64
	Slug  string
	Title string
	Brief string
	// ParentCard is the Jira key of the card this initiative's card is attached to; empty means none.
	ParentCard string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	// ArchivedAt is zero when the initiative is not archived.
	ArchivedAt time.Time
}

// Milestone groups ordered tasks and is the unit of a branch and pull request.
type Milestone struct {
	ID           int64
	InitiativeID int64
	Slug         string
	Title        string
	Description  string
	Position     int
	Git          MilestoneGit
	PullRequest  PullRequestRecord
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// MilestoneGit is the git state the loop recorded for a milestone. Empty
// fields mean not recorded; commits are full object names.
type MilestoneGit struct {
	Branch     string
	BaseCommit string
	HeadCommit string
}

// PullRequestRecord identifies the pull request opened for a milestone. The
// zero value means none is recorded.
type PullRequestRecord struct {
	Provider   string
	Namespace  string
	Repository string
	Number     int
}

// IsZero reports whether no pull request is recorded.
func (r PullRequestRecord) IsZero() bool { return r == PullRequestRecord{} }

// Task is a single loopable unit of work.
type Task struct {
	ID                 int64
	MilestoneID        int64
	Title              string
	Description        string
	AcceptanceCriteria string
	Status             Status
	Position           int
	FailureCount       int
	BlockedReason      string
	Commit             string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// NoteKind classifies a task note.
type NoteKind string

const (
	NoteGeneral   NoteKind = "NOTE"
	NoteKickback  NoteKind = "KICKBACK"
	NoteIteration NoteKind = "ITERATION"
)

// ParseNoteKind accepts a note kind case-insensitively.
func ParseNoteKind(s string) (NoteKind, error) {
	k := NoteKind(strings.ToUpper(strings.TrimSpace(s)))
	switch k {
	case NoteGeneral, NoteKickback, NoteIteration:
		return k, nil
	}
	return "", fmt.Errorf("%w: unknown note kind %q (expected NOTE, KICKBACK, or ITERATION)", ErrInvalid, s)
}

// TaskNote carries context about a task across fresh-context iterations.
type TaskNote struct {
	ID        int64
	TaskID    int64
	Kind      NoteKind
	Body      string
	CreatedAt time.Time
}

// DocKind classifies a document.
type DocKind string

const (
	DocBrief        DocKind = "brief"
	DocArchitecture DocKind = "architecture"
	DocDesign       DocKind = "design"
	DocDecision     DocKind = "decision"
	DocReference    DocKind = "reference"
	DocNote         DocKind = "note"
)

// DocKinds returns every document kind.
func DocKinds() []DocKind {
	return []DocKind{DocBrief, DocArchitecture, DocDesign, DocDecision, DocReference, DocNote}
}

// ParseDocKind accepts a document kind case-insensitively.
func ParseDocKind(s string) (DocKind, error) {
	k := DocKind(strings.ToLower(strings.TrimSpace(s)))
	for _, known := range DocKinds() {
		if k == known {
			return k, nil
		}
	}
	names := make([]string, 0, len(DocKinds()))
	for _, d := range DocKinds() {
		names = append(names, string(d))
	}
	return "", fmt.Errorf("%w: unknown document kind %q (expected one of %s)", ErrInvalid, s, strings.Join(names, ", "))
}

// Document is architecture, design, or other written knowledge. A nil
// InitiativeID scopes it to the whole project.
type Document struct {
	ID           int64
	InitiativeID *int64
	// OriginInitiativeID is the initiative a project-wide document was promoted from, if any.
	OriginInitiativeID *int64
	Kind               DocKind
	Slug               string
	Title              string
	Body               string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	// ArchivedAt is zero when the document is not itself archived.
	ArchivedAt time.Time
}

// Slugify derives a URL- and branch-safe slug from a title.
func Slugify(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.TrimSuffix(b.String(), "-")
	const maxLen = 60
	if len(s) > maxLen {
		s = strings.TrimRight(s[:maxLen], "-")
	}
	return s
}

// ValidateSlug checks a user-supplied slug.
func ValidateSlug(s string) error {
	if s == "" {
		return fmt.Errorf("%w: slug must not be empty", ErrInvalid)
	}
	if Slugify(s) != s {
		return fmt.Errorf("%w: slug %q must be lowercase ASCII letters, digits, and single dashes", ErrInvalid, s)
	}
	return nil
}

// RequireText rejects blank values for a named field.
func RequireText(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s must not be empty", ErrInvalid, field)
	}
	return nil
}

// ValidCommit accepts an empty string (not recorded) or a full SHA-1 or
// SHA-256 object name in lowercase hex.
func ValidCommit(s string) error {
	if s == "" {
		return nil
	}
	if len(s) != 40 && len(s) != 64 {
		return fmt.Errorf("%w: commit %q must be a full 40- or 64-character hash", ErrInvalid, s)
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return fmt.Errorf("%w: commit %q must be lowercase hexadecimal", ErrInvalid, s)
		}
	}
	return nil
}

// Card is the record of a resource's card in an external task manager. An empty Key means the card has not been
// created. A zero AttemptedAt or SyncedAt means never.
type Card struct {
	Kind        Kind
	EntityID    int64
	Provider    string
	Key         string
	URL         string
	ContentHash string
	LastError   string
	AttemptedAt time.Time
	SyncedAt    time.Time
}
