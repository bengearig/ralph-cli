// Package workflow holds the RALPH workflows as one shared source and renders
// them for each target: Claude Code command files and neutral MCP prompts.
package workflow

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"text/template"
)

//go:embed workflows/*.md.tmpl
var sources embed.FS

const (
	sourceDir  = "workflows"
	sourceExt  = ".md.tmpl"
	noArgument = "(none given)"
)

// Target selects which rendering of a workflow to produce.
type Target string

const (
	ClaudeCode Target = "claude_code"
	Neutral    Target = "neutral"
)

// Data is the template input. Args is used only by the neutral target.
type Data struct {
	Target Target
	Args   string
}

// Workflow is a workflow's metadata from its source header.
type Workflow struct {
	Name                string
	Description         string
	Argument            string
	ArgumentHint        string
	ArgumentDescription string
}

type set struct {
	list     []Workflow
	bodies   map[string]string
	partials []string
}

var shipped = func() *set {
	s, err := load(sources)
	if err != nil {
		panic(err)
	}
	return s
}()

// load parses every source in fsys and checks that each one renders for both
// targets, which also proves every cmd reference names an existing workflow.
func load(fsys fs.FS) (*set, error) {
	files, err := fs.Glob(fsys, sourceDir+"/*"+sourceExt)
	if err != nil {
		return nil, err
	}
	s := &set{bodies: map[string]string{}}
	for _, f := range files {
		name := strings.TrimSuffix(strings.TrimPrefix(f, sourceDir+"/"), sourceExt)
		raw, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(name, "_") {
			s.partials = append(s.partials, string(raw))
			continue
		}
		w, body, err := parseSource(name, string(raw))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		s.list = append(s.list, w)
		s.bodies[name] = body
	}
	sort.Slice(s.list, func(i, j int) bool { return s.list[i].Name < s.list[j].Name })
	for _, w := range s.list {
		for _, t := range []Target{ClaudeCode, Neutral} {
			if _, err := s.render(w.Name, Data{Target: t, Args: "x"}); err != nil {
				return nil, err
			}
		}
	}
	return s, nil
}

func (s *set) has(name string) bool {
	_, ok := s.bodies[name]
	return ok
}

func (s *set) render(name string, d Data) (string, error) {
	body, ok := s.bodies[name]
	if !ok {
		return "", fmt.Errorf("unknown workflow %q", name)
	}
	if d.Target != ClaudeCode && d.Target != Neutral {
		return "", fmt.Errorf("unknown target %q", d.Target)
	}
	claude := d.Target == ClaudeCode
	funcs := template.FuncMap{
		"args": func() string {
			switch {
			case claude:
				return "$ARGUMENTS"
			case d.Args == "":
				return noArgument
			}
			return d.Args
		},
		"cmd": func(n string) (string, error) {
			if !s.has(n) {
				return "", fmt.Errorf("cmd references unknown workflow %q", n)
			}
			if claude {
				return "`/ralph:" + n + "`", nil
			}
			return "the `" + n + "` workflow (MCP prompt `" + n + "`)", nil
		},
		"claudeCode": func() bool { return claude },
	}
	t, err := template.New(name).Funcs(funcs).Parse(body)
	if err != nil {
		return "", fmt.Errorf("workflow %s: %w", name, err)
	}
	for i, p := range s.partials {
		if _, err := t.New(fmt.Sprintf("_partial%d", i)).Parse(p); err != nil {
			return "", fmt.Errorf("workflow partial: %w", err)
		}
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, d); err != nil {
		return "", fmt.Errorf("workflow %s: %w", name, err)
	}
	return buf.String(), nil
}

// List returns the workflows sorted by name.
func List() []Workflow {
	return append([]Workflow(nil), shipped.list...)
}

// Render renders a workflow for the target in d.
func Render(name string, d Data) (string, error) {
	return shipped.render(name, d)
}

// RenderCommand returns the Claude Code command file for a workflow.
func RenderCommand(name string) ([]byte, error) {
	return shipped.renderCommand(name)
}

func (s *set) renderCommand(name string) ([]byte, error) {
	var w *Workflow
	for i := range s.list {
		if s.list[i].Name == name {
			w = &s.list[i]
		}
	}
	if w == nil {
		return nil, fmt.Errorf("unknown workflow %q", name)
	}
	body, err := s.render(name, Data{Target: ClaudeCode})
	if err != nil {
		return nil, err
	}
	return []byte("---\ndescription: " + w.Description + "\nargument-hint: " + w.ArgumentHint + "\n---\n\n" + body), nil
}
