package release

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestChangeLines(t *testing.T) {
	generated := "## What's Changed\n" +
		"* [AIN-3715] M-51: Fall back to the milestone card for tasks without one by @bengearig in https://github.com/bengearig/ralph-cli/pull/20\n" +
		"* [AIN-3716] M-52: Second title by @bengearig in https://github.com/bengearig/ralph-cli/pull/22\n" +
		"\n## New Contributors\n" +
		"* @someone made their first contribution in https://github.com/bengearig/ralph-cli/pull/21\n" +
		"* Other entry under contributors by @x in https://github.com/x/y/pull/1\n" +
		"\n**Full Changelog**: https://github.com/bengearig/ralph-cli/compare/v0.2.0...v0.3.0\n"

	tests := []struct {
		name string
		body string
		want []string
	}{
		{"generated", generated, []string{
			"[AIN-3715] M-51: Fall back to the milestone card for tasks without one",
			"[AIN-3716] M-52: Second title",
		}},
		{"bot author", "* Bump x by @dependabot[bot] in https://github.com/x/y/pull/3", []string{"Bump x"}},
		{"by in title", "* Stand by me by @a in https://github.com/x/y/pull/4", []string{"Stand by me"}},
		{"in in title", "* Fail in CI by @a in https://github.com/x/y/pull/5", []string{"Fail in CI"}},
		{"in in title no author", "* Fail in CI in https://github.com/x/y/pull/5", []string{"Fail in CI"}},
		{"dash bullet", "- Dash item by @a in https://github.com/x/y/pull/6", []string{"Dash item"}},
		{"crlf", "## What's Changed\r\n* One by @a in https://github.com/x/y/pull/7\r\n* Two by @a in http://github.com/x/y/pull/8\r\n", []string{"One", "Two"}},
		{"no author", "* Something in https://github.com/x/y/pull/9", []string{"Something"}},
		{"paragraphs only", "This release fixes things.\n\nThanks all.\n", nil},
		{"empty", "", nil},
		{"indented", "  * Indented by @a in https://github.com/x/y/pull/10\n    - Deeper", []string{"Indented", "Deeper"}},
		{"lower-case contributors heading", "### new contributors\n* @a joined\n### Other\n* Kept", []string{"Kept"}},
		{"first contribution outside section", "* @a made their first contribution in https://github.com/x/y/pull/11\n* Kept", []string{"Kept"}},
		{"plain entry", "* Plain", []string{"Plain"}},
		{"empty entry dropped", "* \n*   \n- ", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ChangeLines(tt.body); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ChangeLines() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestNewChangelog(t *testing.T) {
	c := NewChangelog(Release{Tag: "v0.2.0", Body: "* A by @a in https://x.y/1"})
	if c.Version != "v0.2.0" || !reflect.DeepEqual(c.Changes, []string{"A"}) {
		t.Errorf("NewChangelog = %#v", c)
	}

	empty := NewChangelog(Release{Tag: "v0.1.0", Body: "Just prose."})
	if empty.Changes == nil || len(empty.Changes) != 0 {
		t.Fatalf("Changes = %#v, want empty non-nil", empty.Changes)
	}
	data, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"changes":[]`) || !strings.Contains(string(data), `"version":"v0.1.0"`) {
		t.Errorf("json = %s", data)
	}
}

func TestFormatChangelog(t *testing.T) {
	tests := []struct {
		name string
		in   []Changelog
		want string
	}{
		{"two versions", []Changelog{
			{Version: "v0.2.0", Changes: []string{"First"}},
			{Version: "v0.3.0", Changes: []string{"Second", "Third"}},
		}, "v0.2.0\n  - First\nv0.3.0\n  - Second\n  - Third\n"},
		{"no entries", []Changelog{{Version: "v0.2.0", Changes: []string{}}}, "v0.2.0\n  no release notes\n"},
		{"nil entries", []Changelog{{Version: "v0.2.0"}}, "v0.2.0\n  no release notes\n"},
		{"empty list", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatChangelog(tt.in); got != tt.want {
				t.Errorf("FormatChangelog() = %q, want %q", got, tt.want)
			}
		})
	}
}
