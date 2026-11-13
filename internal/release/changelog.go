package release

import (
	"regexp"
	"strings"
)

var (
	authorSuffix = regexp.MustCompile(` by @\S+ in https?://\S+$`)
	urlSuffix    = regexp.MustCompile(` in https?://\S+$`)
)

// Changelog is the condensed notes of one release.
type Changelog struct {
	Version string   `json:"version"`
	Changes []string `json:"changes"`
}

// NewChangelog condenses a release's body into a Changelog.
func NewChangelog(r Release) Changelog {
	changes := ChangeLines(r.Body)
	if changes == nil {
		changes = []string{}
	}
	return Changelog{Version: r.Tag, Changes: changes}
}

// ChangeLines returns the list entries of a release body, minus the trailing
// author and link, and minus new-contributor entries.
func ChangeLines(body string) []string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	var out []string
	skip := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if strings.HasPrefix(trimmed, "#") {
			skip = strings.Contains(strings.ToLower(trimmed), "new contributors")
			continue
		}
		if skip || !(strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "- ")) {
			continue
		}
		entry := strings.TrimSpace(trimmed[2:])
		if strings.Contains(entry, "made their first contribution") {
			continue
		}
		if loc := authorSuffix.FindStringIndex(entry); loc != nil {
			entry = entry[:loc[0]]
		} else if loc := urlSuffix.FindStringIndex(entry); loc != nil {
			entry = entry[:loc[0]]
		}
		if entry = strings.TrimSpace(entry); entry != "" {
			out = append(out, entry)
		}
	}
	return out
}

// FormatChangelog renders changelogs as text, one block per version.
func FormatChangelog(list []Changelog) string {
	var b strings.Builder
	for _, c := range list {
		b.WriteString(c.Version + "\n")
		if len(c.Changes) == 0 {
			b.WriteString("  no release notes\n")
			continue
		}
		for _, change := range c.Changes {
			b.WriteString("  - " + change + "\n")
		}
	}
	return b.String()
}
