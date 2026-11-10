package mcpserver

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/bengearig/ralph/internal/service"
)

func TestMilestoneDependsOnTools(t *testing.T) {
	cs := connect(t)
	mustOK(t, cs, "initiative_create", map[string]any{"title": "Init"}, nil)
	for _, title := range []string{"One", "Two", "Three"} {
		mustOK(t, cs, "milestone_create", map[string]any{"initiative_id": "I-1", "title": title}, nil)
	}

	var v service.MilestoneView
	mustOK(t, cs, "milestone_create", map[string]any{"initiative_id": "I-1", "title": "Four", "depends_on": []string{"M-1"}}, &v)
	if !slices.Equal(v.DependsOn, []string{"M-1"}) {
		t.Fatalf("create depends_on = %v", v.DependsOn)
	}

	mustOK(t, cs, "milestone_update", map[string]any{"id": "M-4", "title": "Four!"}, &v)
	if !slices.Equal(v.DependsOn, []string{"M-1"}) {
		t.Fatalf("omitted depends_on changed the set: %v", v.DependsOn)
	}
	v = service.MilestoneView{}
	mustOK(t, cs, "milestone_update", map[string]any{"id": "M-4", "depends_on": []string{}}, &v)
	if len(v.DependsOn) != 0 {
		t.Fatalf("empty depends_on did not clear: %v", v.DependsOn)
	}
	mustOK(t, cs, "milestone_update", map[string]any{"id": "M-4", "depends_on": []string{"M-2"}}, &v)
	if !slices.Equal(v.DependsOn, []string{"M-2"}) {
		t.Fatalf("depends_on did not replace: %v", v.DependsOn)
	}

	var got milestoneDetail
	mustOK(t, cs, "milestone_get", map[string]any{"id": "M-4"}, &got)
	if !slices.Equal(got.DependsOn, []string{"M-2"}) {
		t.Fatalf("milestone_get depends_on = %v", got.DependsOn)
	}

	mustOK(t, cs, "milestone_update", map[string]any{"id": "M-2", "depends_on": []string{"M-3"}}, nil)
	res := call(t, cs, "milestone_update", map[string]any{"id": "M-3", "depends_on": []string{"M-4"}}, nil)
	if !res.IsError || !strings.Contains(text(res), "cycle") {
		t.Fatalf("cycle result = error %v: %s", res.IsError, text(res))
	}
	res = call(t, cs, "milestone_update", map[string]any{"id": "M-3", "depends_on": []string{"bogus"}}, nil)
	if !res.IsError {
		t.Fatal("bad ID accepted")
	}
}

func TestMilestoneDependsOnSchemas(t *testing.T) {
	cs := connect(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tool := range res.Tools {
		if tool.Name != "milestone_create" && tool.Name != "milestone_update" {
			continue
		}
		raw, _ := json.Marshal(tool.InputSchema)
		var s struct {
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		if s.Properties["depends_on"].Description == "" {
			t.Errorf("%s: depends_on missing or undescribed in %s", tool.Name, raw)
		}
		if !strings.Contains(tool.Description, "depends_on") {
			t.Errorf("%s description does not mention depends_on", tool.Name)
		}
		seen++
	}
	if seen != 2 {
		t.Fatalf("saw %d tools", seen)
	}
}
