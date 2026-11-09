package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/domain"
	"github.com/bengearig/ralph/internal/service"
)

// optString returns &v only when the flag was explicitly set, so patches
// distinguish "unchanged" from "set to empty".
func optString(cmd *cobra.Command, name, v string) *string {
	if cmd.Flags().Changed(name) {
		return &v
	}
	return nil
}

func optInt(cmd *cobra.Command, name string, v int) *int {
	if cmd.Flags().Changed(name) {
		return &v
	}
	return nil
}

// textFlag resolves a value given inline or via a file flag; "-" reads stdin.
// It returns nil when neither flag was set.
func (a *app) textFlag(cmd *cobra.Command, inlineName, inline, fileName, file string) (*string, error) {
	inlineSet, fileSet := cmd.Flags().Changed(inlineName), cmd.Flags().Changed(fileName)
	switch {
	case inlineSet && fileSet:
		return nil, fmt.Errorf("use either --%s or --%s, not both", inlineName, fileName)
	case inlineSet:
		return &inline, nil
	case fileSet:
		var data []byte
		var err error
		if file == "-" {
			data, err = io.ReadAll(a.streams.In)
		} else {
			data, err = os.ReadFile(file)
		}
		if err != nil {
			return nil, fmt.Errorf("reading --%s: %w", fileName, err)
		}
		s := string(data)
		return &s, nil
	}
	return nil, nil
}

func parseIDArg(k domain.Kind, s string) (int64, error) {
	return domain.ParseID(k, s)
}

// parseIDFlag parses an optional ID flag; empty means zero (unfiltered).
func parseIDFlag(k domain.Kind, s string) (int64, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	return domain.ParseID(k, s)
}

func parseStatuses(raw []string) ([]domain.Status, error) {
	var out []domain.Status
	for _, r := range raw {
		for _, part := range strings.Split(r, ",") {
			if strings.TrimSpace(part) == "" {
				continue
			}
			st, err := domain.ParseStatus(part)
			if err != nil {
				return nil, err
			}
			out = append(out, st)
		}
	}
	return out, nil
}

func parseKinds(raw []string) ([]domain.Kind, error) {
	var out []domain.Kind
	for _, r := range raw {
		for _, part := range strings.Split(r, ",") {
			if strings.TrimSpace(part) == "" {
				continue
			}
			k, err := domain.ParseKind(part)
			if err != nil {
				return nil, err
			}
			out = append(out, k)
		}
	}
	return out, nil
}

// warn writes a non-fatal warning to stderr.
func (a *app) warn(msg string) {
	if msg != "" {
		fmt.Fprintln(a.streams.Err, "warning:", msg)
	}
}

// section prints a labeled multi-line block, skipping empty values.
func (a *app) section(label, body string) error {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	return a.printf("\n%s:\n%s\n", label, indent(body))
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}

func shortCommit(hash string) string {
	const n = 12
	if len(hash) > n {
		return hash[:n]
	}
	return hash
}

// branchLine renders a recorded branch with whichever commits are known.
func branchLine(m service.MilestoneView) string {
	var commits []string
	if m.BaseCommit != "" {
		commits = append(commits, "base "+shortCommit(m.BaseCommit))
	}
	if m.HeadCommit != "" {
		commits = append(commits, "head "+shortCommit(m.HeadCommit))
	}
	if len(commits) == 0 {
		return m.Branch
	}
	return m.Branch + " (" + strings.Join(commits, ", ") + ")"
}
