package service

import (
	"errors"
	"fmt"
	"strings"

	"github.com/bengearig/ralph/internal/domain"
)

// ErrDocumentChoiceRequired is returned when archiving an initiative that has
// documents to decide about and no choice was given.
var ErrDocumentChoiceRequired = errors.New("document choice required")

// DocumentChoice says which of an initiative's documents to promote when it is
// archived. The zero value means no choice was made.
type DocumentChoice struct {
	All  bool
	None bool
	IDs  []int64
}

// ParseDocumentChoice turns "all", "none", or document IDs into a choice.
// Keywords cannot be mixed with IDs; an empty list means no choice.
func ParseDocumentChoice(items []string) (DocumentChoice, error) {
	var c DocumentChoice
	keywords := 0
	for _, raw := range items {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "all":
			c.All = true
			keywords++
		case "none":
			c.None = true
			keywords++
		default:
			id, err := domain.ParseID(domain.KindDocument, raw)
			if err != nil {
				return DocumentChoice{}, err
			}
			c.IDs = append(c.IDs, id)
		}
	}
	if keywords > 0 && (keywords > 1 || len(c.IDs) > 0) {
		return DocumentChoice{}, fmt.Errorf("%w: \"all\" and \"none\" stand alone and cannot be combined with each other or with document IDs", domain.ErrInvalid)
	}
	return c, nil
}

func (c DocumentChoice) chosen() bool { return c.All || c.None || len(c.IDs) > 0 }

// DocumentSummary identifies a candidate document.
type DocumentSummary struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
}

// DocumentChoiceError lists the documents an archive needs a decision about.
type DocumentChoiceError struct {
	Initiative string
	Documents  []DocumentSummary
	Suggested  []DocumentSummary
}

func newDocumentChoiceError(i domain.Initiative, docs []domain.Document) *DocumentChoiceError {
	e := &DocumentChoiceError{Initiative: domain.FormatID(domain.KindInitiative, i.ID)}
	for _, d := range docs {
		s := DocumentSummary{ID: domain.FormatID(domain.KindDocument, d.ID), Kind: string(d.Kind), Title: d.Title}
		e.Documents = append(e.Documents, s)
		if d.Kind == domain.DocDesign || d.Kind == domain.DocDecision {
			e.Suggested = append(e.Suggested, s)
		}
	}
	return e
}

// SuggestedIDs joins the suggested document IDs with commas; empty when there are none.
func (e *DocumentChoiceError) SuggestedIDs() string {
	ids := make([]string, len(e.Suggested))
	for n, d := range e.Suggested {
		ids[n] = d.ID
	}
	return strings.Join(ids, ",")
}

func (e *DocumentChoiceError) Is(target error) bool { return target == ErrDocumentChoiceRequired }

func (e *DocumentChoiceError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s has documents that stay hidden once it is archived; choose which to promote to project-wide:", e.Initiative)
	for _, d := range e.Documents {
		fmt.Fprintf(&b, "\n  %s  %s  %q", d.ID, d.Kind, d.Title)
	}
	if len(e.Suggested) == 0 {
		b.WriteString("\nSuggested: none (no design or decision documents)")
	} else {
		ids := make([]string, len(e.Suggested))
		for n, d := range e.Suggested {
			ids[n] = d.ID
		}
		fmt.Fprintf(&b, "\nSuggested (design and decision documents): %s", strings.Join(ids, ", "))
	}
	return b.String()
}
