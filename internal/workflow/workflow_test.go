package workflow

import (
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func TestRenderCommandMatchesGoldens(t *testing.T) {
	ws := List()
	if len(ws) == 0 {
		t.Fatal("no workflows")
	}
	for _, w := range ws {
		want, err := os.ReadFile(filepath.Join("testdata", w.Name+".md"))
		if err != nil {
			t.Fatalf("%s: %v", w.Name, err)
		}
		got, err := RenderCommand(w.Name)
		if err != nil {
			t.Fatalf("%s: %v", w.Name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s: rendered command differs from testdata/%s.md", w.Name, w.Name)
		}
	}
	goldens, _ := filepath.Glob("testdata/*.md")
	goldens = slices.DeleteFunc(goldens, func(f string) bool { return strings.HasSuffix(f, ".neutral.md") })
	if len(goldens) != len(ws) {
		t.Errorf("%d goldens for %d workflows", len(goldens), len(ws))
	}
}

func TestListSortedWithMetadata(t *testing.T) {
	want := map[string]string{
		"articulate": "scope", "import": "paths", "receive": "instructions", "reconcile": "milestone",
		"search": "query", "status": "initiative",
	}
	var names []string
	for _, w := range List() {
		names = append(names, w.Name)
		if arg, ok := want[w.Name]; !ok || w.Argument != arg {
			t.Errorf("%s: argument %q, want %q", w.Name, w.Argument, arg)
		}
		if w.Description == "" || w.ArgumentHint == "" || w.ArgumentDescription == "" {
			t.Errorf("%s: incomplete metadata %+v", w.Name, w)
		}
	}
	if !slices.IsSorted(names) {
		t.Errorf("not sorted: %v", names)
	}
	if len(names) != len(want) {
		t.Errorf("names = %v", names)
	}
}

func fixture(files map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for name, content := range files {
		m["workflows/"+name+".md.tmpl"] = &fstest.MapFile{Data: []byte(content)}
	}
	return m
}

const validHeader = "---\ndescription: d\nargument: a\nargument-hint: \"<h>\"\nargument-description: ad\n---\n\n"

func TestLoadValidation(t *testing.T) {
	cases := map[string]struct {
		files map[string]string
		want  string
	}{
		"ok":              {map[string]string{"one": validHeader + "x {{args}} {{cmd \"one\"}}"}, ""},
		"missing key":     {map[string]string{"one": "---\ndescription: d\nargument: a\nargument-hint: h\n---\nx"}, "missing key \"argument-description\""},
		"unknown key":     {map[string]string{"one": strings.Replace(validHeader, "---\n\n", "extra: 1\n---\n\n", 1)}, "unknown key \"extra\""},
		"duplicate key":   {map[string]string{"one": strings.Replace(validHeader, "---\n\n", "argument: b\n---\n\n", 1)}, "duplicate key"},
		"no header":       {map[string]string{"one": "body"}, "must start with"},
		"unclosed":        {map[string]string{"one": "---\ndescription: d"}, "not closed"},
		"missing cmd":     {map[string]string{"one": validHeader + "{{cmd \"nope\"}}"}, "unknown workflow \"nope\""},
		"bad template":    {map[string]string{"one": validHeader + "{{if}}"}, "workflow one"},
		"cmd to partial":  {map[string]string{"one": validHeader + "{{cmd \"_p\"}}", "_p": "{{define \"p\"}}x{{end}}"}, "unknown workflow \"_p\""},
		"partial allowed": {map[string]string{"one": validHeader + "{{template \"p\"}}", "_p": "{{define \"p\"}}x{{end}}"}, ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := load(fixture(c.files))
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("error = %v, want containing %q", err, c.want)
			}
		})
	}
}

func TestNeutralRendering(t *testing.T) {
	for _, w := range List() {
		out, err := Render(w.Name, Data{Target: Neutral})
		if err != nil {
			t.Fatalf("%s: %v", w.Name, err)
		}
		for _, bad := range []string{"EnterPlanMode", "ExitPlanMode", "$ARGUMENTS", "/ralph:", "slash command"} {
			if strings.Contains(out, bad) {
				t.Errorf("%s: neutral rendering contains %q", w.Name, bad)
			}
		}
		if !strings.Contains(out, noArgument) {
			t.Errorf("%s: empty argument not rendered as %q", w.Name, noArgument)
		}
		given, err := Render(w.Name, Data{Target: Neutral, Args: "UNIQUE-ARG-VALUE"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(given, "UNIQUE-ARG-VALUE") {
			t.Errorf("%s: argument not substituted", w.Name)
		}
	}
}

func TestRenderErrors(t *testing.T) {
	if _, err := Render("nope", Data{Target: Neutral}); err == nil {
		t.Error("unknown workflow: want error")
	}
	if _, err := Render("receive", Data{Target: "other"}); err == nil {
		t.Error("unknown target: want error")
	}
	if _, err := RenderCommand("nope"); err == nil {
		t.Error("unknown command: want error")
	}
}

var update = flag.Bool("update", false, "rewrite testdata/*.neutral.md snapshots")

func TestNeutralApprovalWording(t *testing.T) {
	for _, name := range []string{"receive", "reconcile", "import"} {
		out, err := Render(name, Data{Target: Neutral})
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"plan or approval mode", "structured question tool", "explicitly confirm", "apply nothing"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: neutral rendering lacks %q", name, want)
			}
		}
	}
}

func TestNeutralSnapshots(t *testing.T) {
	for _, w := range List() {
		got, err := Render(w.Name, Data{Target: Neutral})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join("testdata", w.Name+".neutral.md")
		if *update {
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (run go test ./internal/workflow -update)", w.Name, err)
		}
		if got != string(want) {
			t.Errorf("%s: neutral rendering differs from %s (run go test ./internal/workflow -update)", w.Name, path)
		}
	}
}

func TestImportWorkflowContent(t *testing.T) {
	w := slices.IndexFunc(List(), func(w Workflow) bool { return w.Name == "import" })
	if w < 0 {
		t.Fatal("import workflow missing")
	}
	got := List()[w]
	if got.Description != "RALPH import — integrate queued files into ralph's documents, resolving contradictions with you" ||
		got.Argument != "paths" ||
		got.ArgumentHint != `"[files or directories to queue first; empty works the existing queue]"` ||
		got.ArgumentDescription != "Optional files or directories to queue before working the queue." {
		t.Errorf("header = %+v", got)
	}
	cmd, err := RenderCommand("import")
	if err != nil {
		t.Fatal(err)
	}
	neutral, err := Render("import", Data{Target: Neutral})
	if err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{"claude_code": string(cmd), "neutral": neutral} {
		for _, want := range []string{"import_queue", "import_list", "import_get", "import_resolve", "deferred", "preserve", "decision", "KICKBACK", "no deletes"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s rendering lacks %q", name, want)
			}
		}
	}
}

func TestImportWorkflowTrackerSection(t *testing.T) {
	cmd, err := RenderCommand("import")
	if err != nil {
		t.Fatal(err)
	}
	neutral, err := Render("import", Data{Target: Neutral})
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{
		"TRACKER.md", "ralph-loop", "TODO.md", "TASKS.md", "PLAN.md", "Jira", "GitHub", "GitLab", "Linear",
		"Trello", "Asana", "Notion", "Azure DevOps", "Backlog", "Taskwarrior",
	}
	for name, out := range map[string]string{"claude_code": string(cmd), "neutral": neutral} {
		for _, want := range []string{
			"task_import", "initiative_create", "milestone_create", "confirm_outline", "PROPOSED",
			"never `IN_PROGRESS`", "Imported from <path>.", "imported as I-n", "use `PROPOSED` and say so",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s rendering lacks %q", name, want)
			}
		}
		for _, bad := range forbidden {
			if strings.Contains(strings.ToLower(out), strings.ToLower(bad)) {
				t.Errorf("%s rendering names the tool or file %q", name, bad)
			}
		}
	}
}

func TestArticulateNarrowCheckGuidance(t *testing.T) {
	cmd, err := RenderCommand("articulate")
	if err != nil {
		t.Fatal(err)
	}
	neutral, err := Render("articulate", Data{Target: Neutral})
	if err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{"claude_code": string(cmd), "neutral": neutral} {
		for _, want := range []string{"names the narrowest command that proves the task", "not the whole suite", "the last task of a", "milestone's final check"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s rendering lacks %q", name, want)
			}
		}
	}
}

func TestReceiveParentCardGuidance(t *testing.T) {
	cmd, err := RenderCommand("receive")
	if err != nil {
		t.Fatal(err)
	}
	neutral, err := Render("receive", Data{Target: Neutral})
	if err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{"claude_code": string(cmd), "neutral": neutral} {
		for _, want := range []string{
			"pass its key as `parent_card`",
			"`parent_card` out",
			"Never guess a key",
			"passing the same `parent_card`",
			"the parent card and the sprint when the user named them",
			"`initiative_update` with `parent_card`",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s rendering lacks %q", name, want)
			}
		}
	}
}

func TestReceiveSprintGuidance(t *testing.T) {
	cmd, err := RenderCommand("receive")
	if err != nil {
		t.Fatal(err)
	}
	neutral, err := Render("receive", Data{Target: Neutral})
	if err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{"claude_code": string(cmd), "neutral": neutral} {
		for _, want := range []string{
			"pass the name as `sprint`",
			"which means the board's active sprint",
			"Never guess a sprint name",
			"passing the same `parent_card` and `sprint`",
			"the parent card and the sprint when the user named them",
			"`initiative_update` with `sprint`",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s rendering lacks %q", name, want)
			}
		}
	}
}
