package adf

import (
	"encoding/json"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

func decode(t *testing.T, raw []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("invalid JSON %s: %v", raw, err)
	}
	return v
}

func doc(content string) string {
	return `{"version":1,"type":"doc","content":[` + content + `]}`
}

func TestFromMarkdown(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"paragraph", "hello world", doc(`{"type":"paragraph","content":[{"type":"text","text":"hello world"}]}`)},
		{"two paragraphs", "one\n\ntwo", doc(`{"type":"paragraph","content":[{"type":"text","text":"one"}]},{"type":"paragraph","content":[{"type":"text","text":"two"}]}`)},
		{"hard break", "one\ntwo", doc(`{"type":"paragraph","content":[{"type":"text","text":"one"},{"type":"hardBreak"},{"type":"text","text":"two"}]}`)},
		{"heading 1", "# Title", doc(`{"type":"heading","attrs":{"level":1},"content":[{"type":"text","text":"Title"}]}`)},
		{"heading 6", "###### Small", doc(`{"type":"heading","attrs":{"level":6},"content":[{"type":"text","text":"Small"}]}`)},
		{"bullet list", "- a\n* b\n+ c", doc(`{"type":"bulletList","content":[` +
			`{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"a"}]}]},` +
			`{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"b"}]}]},` +
			`{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"c"}]}]}]}`)},
		{"ordered list", "1. a\n2) b", doc(`{"type":"orderedList","content":[` +
			`{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"a"}]}]},` +
			`{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"b"}]}]}]}`)},
		{"nested bullets", "- a\n  - b\n    - c\n- d", doc(`{"type":"bulletList","content":[` +
			`{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"a"}]},` +
			`{"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"b"}]},` +
			`{"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"c"}]}]}]}]}]}]},` +
			`{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"d"}]}]}]}`)},
		{"tab nested", "- a\n\t- b", doc(`{"type":"bulletList","content":[` +
			`{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"a"}]},` +
			`{"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"b"}]}]}]}]}]}`)},
		{"continuation", "- first\n  second\n- third", doc(`{"type":"bulletList","content":[` +
			`{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"first"},{"type":"hardBreak"},{"type":"text","text":"second"}]}]},` +
			`{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"third"}]}]}]}`)},
		{"ordered start", "3. a", doc(`{"type":"orderedList","attrs":{"order":3},"content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"a"}]}]}]}`)},
		{"fenced code", "```go\nfmt.Println()\n```", doc(`{"type":"codeBlock","attrs":{"language":"go"},"content":[{"type":"text","text":"fmt.Println()"}]}`)},
		{"tilde fence", "~~~\nx\ny\n~~~", doc(`{"type":"codeBlock","content":[{"type":"text","text":"x\ny"}]}`)},
		{"unterminated fence", "```\nabc\ndef", doc(`{"type":"codeBlock","content":[{"type":"text","text":"abc\ndef"}]}`)},
		{"empty fence", "```\n```", doc(`{"type":"codeBlock"}`)},
		{"code verbatim", "```\n**not bold**\n# not a heading\n```", doc(`{"type":"codeBlock","content":[{"type":"text","text":"**not bold**\n# not a heading"}]}`)},
		{"block quote", "> one\n> two", doc(`{"type":"blockquote","content":[{"type":"paragraph","content":[{"type":"text","text":"one"},{"type":"hardBreak"},{"type":"text","text":"two"}]}]}`)},
		{"block quote paragraphs", "> one\n>\n> two", doc(`{"type":"blockquote","content":[{"type":"paragraph","content":[{"type":"text","text":"one"}]},{"type":"paragraph","content":[{"type":"text","text":"two"}]}]}`)},
		{"rule", "---", doc(`{"type":"rule"}`)},
		{"rule stars", "a\n\n***\n\n___", doc(`{"type":"paragraph","content":[{"type":"text","text":"a"}]},{"type":"rule"},{"type":"rule"}`)},

		{"bold stars", "**bold**", doc(`{"type":"paragraph","content":[{"type":"text","text":"bold","marks":[{"type":"strong"}]}]}`)},
		{"bold underscores", "__bold__", doc(`{"type":"paragraph","content":[{"type":"text","text":"bold","marks":[{"type":"strong"}]}]}`)},
		{"italic star", "a *it* b", doc(`{"type":"paragraph","content":[{"type":"text","text":"a "},{"type":"text","text":"it","marks":[{"type":"em"}]},{"type":"text","text":" b"}]}`)},
		{"italic underscore", "_it_", doc(`{"type":"paragraph","content":[{"type":"text","text":"it","marks":[{"type":"em"}]}]}`)},
		{"bold italic", "***both***", doc(`{"type":"paragraph","content":[{"type":"text","text":"both","marks":[{"type":"strong"},{"type":"em"}]}]}`)},
		{"inline code", "use `x * y` here", doc(`{"type":"paragraph","content":[{"type":"text","text":"use "},{"type":"text","text":"x * y","marks":[{"type":"code"}]},{"type":"text","text":" here"}]}`)},
		{"link", "[Go](https://go.dev)", doc(`{"type":"paragraph","content":[{"type":"text","text":"Go","marks":[{"type":"link","attrs":{"href":"https://go.dev"}}]}]}`)},
		{"link in bold", "**[Go](https://go.dev)**", doc(`{"type":"paragraph","content":[{"type":"text","text":"Go","marks":[{"type":"strong"},{"type":"link","attrs":{"href":"https://go.dev"}}]}]}`)},
		{"escape", `\*not italic\*`, doc(`{"type":"paragraph","content":[{"type":"text","text":"*not italic*"}]}`)},
		{"snake case", "snake_case_name", doc(`{"type":"paragraph","content":[{"type":"text","text":"snake_case_name"}]}`)},

		{"javascript link", "[click](javascript:alert(1))", doc(`{"type":"paragraph","content":[{"type":"text","text":"click"}]}`)},
		{"mailto link", "[me](mailto:a@b.co)", doc(`{"type":"paragraph","content":[{"type":"text","text":"me","marks":[{"type":"link","attrs":{"href":"mailto:a@b.co"}}]}]}`)},

		{"empty", "", doc(``)},
		{"whitespace", " \n\t\n ", doc(``)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decode(t, FromMarkdown(tt.in).JSON())
			want := decode(t, []byte(tt.want))
			if !reflect.DeepEqual(got, want) {
				t.Errorf("FromMarkdown(%q)\n got: %s\nwant: %s", tt.in, FromMarkdown(tt.in).JSON(), tt.want)
			}
		})
	}
}

func TestEmpty(t *testing.T) {
	for _, in := range []string{"", "  ", "\n\n", "\t \r\n"} {
		d := FromMarkdown(in)
		if !d.Empty() {
			t.Errorf("FromMarkdown(%q) not empty", in)
		}
		if !json.Valid(d.JSON()) {
			t.Errorf("FromMarkdown(%q) invalid JSON", in)
		}
	}
	if FromMarkdown("x").Empty() {
		t.Error("non-empty input reported empty")
	}
}

// plainText flattens paragraphs and inline nodes back to text.
func plainText(nodes []Node) string {
	var parts []string
	for _, n := range nodes {
		parts = append(parts, nodeText(n))
	}
	return strings.Join(parts, "\n\n")
}

func nodeText(n Node) string {
	switch n.Type {
	case "text":
		return n.Text
	case "hardBreak":
		return "\n"
	}
	var b strings.Builder
	for _, c := range n.Content {
		b.WriteString(nodeText(c))
	}
	return b.String()
}

func TestFallbackKeepsText(t *testing.T) {
	for _, in := range []string{
		"| a | b |\n|---|---|\n| 1 | 2 |",
		"<b>html</b>",
		"![alt](u)",
		"- [ ] checkbox",
		"- [x] done",
	} {
		d := FromMarkdown(in)
		if len(d.Content) != 1 || d.Content[0].Type != "paragraph" {
			t.Errorf("%q: want one paragraph, got %s", in, d.JSON())
			continue
		}
		if got := nodeText(d.Content[0]); got != in {
			t.Errorf("%q: text = %q", in, got)
		}
	}
}

func checkStructure(t *testing.T, in string, nodes []Node, inListItem bool) {
	t.Helper()
	blocks := map[string]bool{"paragraph": true, "heading": true, "bulletList": true, "orderedList": true, "codeBlock": true, "blockquote": true, "rule": true}
	for _, n := range nodes {
		if n.Type == "text" && n.Text == "" {
			t.Errorf("%q: empty text node", in)
		}
		if n.Type == "codeBlock" && len(n.Content) > 1 {
			t.Errorf("%q: codeBlock with several children", in)
		}
		if n.Type == "listItem" {
			if len(n.Content) == 0 {
				t.Errorf("%q: empty listItem", in)
			}
			for _, c := range n.Content {
				if !blocks[c.Type] {
					t.Errorf("%q: listItem child %q is not a block", in, c.Type)
				}
			}
		}
		checkStructure(t, in, n.Content, false)
	}
}

func TestStructureRules(t *testing.T) {
	for _, in := range []string{
		"- a\n-\n- ", "> \n>", "```\n\n```", "1.", "**", "``", "[](x)", "- \n  - ", "# a\n- b\n  c\n\n> d\n> - e",
	} {
		checkStructure(t, in, FromMarkdown(in).Content, false)
	}
}

func TestPlainTextProperty(t *testing.T) {
	words := []string{"alpha", "beta", "gamma", "delta", "x", "Hello,", "world.", "ünï", "42", "a:b"}
	rng := rand.New(rand.NewSource(1))
	for n := 0; n < 300; n++ {
		var paras []string
		for p := 0; p <= rng.Intn(3); p++ {
			var lines []string
			for l := 0; l <= rng.Intn(3); l++ {
				var ws []string
				for w := 0; w <= rng.Intn(5); w++ {
					ws = append(ws, words[rng.Intn(len(words))])
				}
				lines = append(lines, strings.Join(ws, " "))
			}
			paras = append(paras, strings.Join(lines, "\n"))
		}
		want := strings.Join(paras, "\n\n")
		in := strings.Join(paras, "\n\n\n")
		if got := plainText(FromMarkdown(in).Content); got != want {
			t.Fatalf("input %q: text %q, want %q", in, got, want)
		}
	}
}

var fuzzSeeds = []string{
	"", "# h\n\ntext **b** _i_ `c` [l](https://x.y)", "- a\n  - b\n\t- c\n1. d", "```go\nx", "> q\n> - r", "**[a](b)**",
	"\\", "[", "[a](", "![", "`", "***", "___", "* * *", "- [ ] t", "| a |\n|---|", "\x00\xff", "[[a]](b)", "*a **b** c*",
}

func FuzzFromMarkdown(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		d := FromMarkdown(s)
		if !json.Valid(d.JSON()) {
			t.Fatalf("invalid JSON for %q", s)
		}
		checkStructure(t, s, d.Content, false)
	})
}

func TestSeedsValid(t *testing.T) {
	for _, s := range fuzzSeeds {
		if !json.Valid(FromMarkdown(s).JSON()) {
			t.Errorf("invalid JSON for %q", s)
		}
	}
}
