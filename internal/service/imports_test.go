package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/bengearig/ralph/internal/domain"
)

type progressCall struct {
	done, total int
	path        string
}

func recordProgress(calls *[]progressCall) ImportProgress {
	return func(done, total int, p string) { *calls = append(*calls, progressCall{done, total, p}) }
}

func TestQueueImportsProgress(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	files := []ImportFile{{Path: "a.md", Content: "a"}, {Path: "b.md", Content: "b"}, {Path: "c.md", Content: "c"}}
	var calls []progressCall
	out, err := e.svc.QueueImports(ctx, files, recordProgress(&calls))
	if err != nil {
		t.Fatal(err)
	}
	want := []progressCall{{0, 3, ""}, {1, 3, "a.md"}, {2, 3, "b.md"}, {3, 3, "c.md"}}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %+v, want %+v", calls, want)
	}
	wantOut := []ImportOutcome{{"a.md", OutcomeNew}, {"b.md", OutcomeNew}, {"c.md", OutcomeNew}}
	if !reflect.DeepEqual(out, wantOut) {
		t.Errorf("out = %+v", out)
	}

	out, err = e.svc.QueueImports(ctx, files, nil)
	if err != nil || len(out) != 3 || out[0].Outcome != OutcomeQueued {
		t.Fatalf("nil progress: out = %+v, err = %v", out, err)
	}
}

func TestQueueImportsProgressInvalidPath(t *testing.T) {
	e := newEnv(t)
	var calls []progressCall
	_, err := e.svc.QueueImports(context.Background(), []ImportFile{{Path: "ok.md", Content: "x"}, {Path: "../bad", Content: "y"}}, recordProgress(&calls))
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
	if len(calls) != 0 {
		t.Errorf("calls = %+v", calls)
	}
}

func TestQueueImportsProgressTxFailure(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls []progressCall
	record := recordProgress(&calls)
	progress := func(done, total int, p string) {
		record(done, total, p)
		if done == 1 {
			cancel()
		}
	}
	files := []ImportFile{{Path: "a.md", Content: "a"}, {Path: "b.md", Content: "b"}}
	out, err := e.svc.QueueImports(ctx, files, progress)
	if err == nil || out != nil {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	want := []progressCall{{0, 2, ""}, {1, 2, "a.md"}}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %+v, want %+v", calls, want)
	}
	if list, _ := e.svc.ListImports(context.Background(), nil); len(list) != 0 {
		t.Errorf("records = %+v", list)
	}
}

func TestImportAsNewProgress(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	opts := AsNewOptions{Kind: domain.DocReference}
	if _, err := e.svc.ImportAsNew(ctx, []ImportFile{{Path: "skip.md", Content: "# S"}, {Path: "refuse.md", Content: "# R"}}, opts); err != nil {
		t.Fatal(err)
	}
	files := []ImportFile{
		{Path: "new.md", Content: "# N"},
		{Path: "skip.md", Content: "# S"},
		{Path: "refuse.md", Content: "# R changed"},
		{Path: "docs/.md", Content: "no title"},
	}
	var calls []progressCall
	opts.Progress = recordProgress(&calls)
	out, err := e.svc.ImportAsNew(ctx, files, opts)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{out[0].Outcome, out[1].Outcome, out[2].Outcome, out[3].Outcome}
	if want := []string{AsNewCreated, AsNewSkipped, AsNewRefused, AsNewFailed}; !reflect.DeepEqual(got, want) {
		t.Fatalf("outcomes = %v", got)
	}
	want := []progressCall{{0, 4, ""}, {1, 4, "new.md"}, {2, 4, "skip.md"}, {3, 4, "refuse.md"}, {4, 4, "docs/.md"}}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %+v, want %+v", calls, want)
	}
}

func TestImportAsNewProgressNoFiles(t *testing.T) {
	e := newEnv(t)
	var calls []progressCall
	out, err := e.svc.ImportAsNew(context.Background(), nil, AsNewOptions{Kind: domain.DocReference, Progress: recordProgress(&calls)})
	if err != nil || len(out) != 0 {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	if want := []progressCall{{0, 0, ""}}; !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %+v", calls)
	}
}
