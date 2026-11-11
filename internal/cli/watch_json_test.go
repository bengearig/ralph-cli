package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

type flushWriter struct {
	recWriter
	flushes       int
	writesAtFlush []int
}

func (w *flushWriter) Flush() error {
	w.flushes++
	w.writesAtFlush = append(w.writesAtFlush, len(w.writes))
	return nil
}

func jsonApp(out, errW *recWriter) *app {
	return &app{streams: Streams{Out: out, Err: errW}}
}

func TestJSONLinesOneLinePerChange(t *testing.T) {
	out := &recWriter{}
	r := jsonApp(out, &recWriter{}).newJSONLinesRenderer()
	s1 := &snapshot{JSON: []byte(`{"a":1}`)}
	s2 := &snapshot{JSON: []byte(`{"a":2}`)}
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	if err := r.Update(nil, s1); err != nil {
		t.Fatal(err)
	}
	if err := r.Tick(time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := r.Update(s1, s2); err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(); err != nil {
		t.Fatal(err)
	}
	want := []string{`{"a":1}` + "\n", `{"a":2}` + "\n"}
	if !reflect.DeepEqual(out.writes, want) {
		t.Fatalf("writes = %q, want %q", out.writes, want)
	}
	if strings.Contains(out.all(), "\x1b") {
		t.Fatal("output contains escape")
	}
	for _, l := range strings.Split(strings.TrimSuffix(out.all(), "\n"), "\n") {
		if !json.Valid([]byte(l)) {
			t.Fatalf("invalid json line %q", l)
		}
	}
}

func TestJSONLinesCompacts(t *testing.T) {
	out := &recWriter{}
	r := jsonApp(out, &recWriter{}).newJSONLinesRenderer()
	in := []byte("{\n  \"a\": [1, 2],\n  \"b\": \"x\"\n}")
	if err := r.Update(nil, &snapshot{JSON: in}); err != nil {
		t.Fatal(err)
	}
	if len(out.writes) != 1 || strings.Count(out.writes[0], "\n") != 1 || !strings.HasSuffix(out.writes[0], "\n") {
		t.Fatalf("writes = %q", out.writes)
	}
	var got, want any
	if err := json.Unmarshal([]byte(out.writes[0]), &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(in, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestJSONLinesInvalidJSONErrors(t *testing.T) {
	out := &recWriter{}
	r := jsonApp(out, &recWriter{}).newJSONLinesRenderer()
	if err := r.Update(nil, &snapshot{JSON: []byte("{\n")}); err == nil {
		t.Fatal("want error")
	}
	if len(out.writes) != 0 {
		t.Fatalf("wrote %q", out.writes)
	}
}

func TestJSONLinesFlush(t *testing.T) {
	w := &flushWriter{}
	a := &app{streams: Streams{Out: w, Err: &recWriter{}}}
	r := a.newJSONLinesRenderer()
	s := &snapshot{JSON: []byte(`{}`)}
	_ = r.Update(nil, s)
	_ = r.Update(s, &snapshot{JSON: []byte(`{"x":1}`)})
	if w.flushes != 2 || !reflect.DeepEqual(w.writesAtFlush, []int{1, 2}) {
		t.Fatalf("flushes=%d at=%v", w.flushes, w.writesAtFlush)
	}
}

func TestJSONLinesNotice(t *testing.T) {
	out, errW := &recWriter{}, &recWriter{}
	r := jsonApp(out, errW).newJSONLinesRenderer()
	if err := r.Notice("waiting: database is locked"); err != nil {
		t.Fatal(err)
	}
	if got := errW.all(); got != "ralph: waiting: database is locked\n" {
		t.Fatalf("err = %q", got)
	}
	errW.reset()
	if err := r.Notice(""); err != nil {
		t.Fatal(err)
	}
	if errW.all() != "" || out.all() != "" {
		t.Fatalf("err=%q out=%q", errW.all(), out.all())
	}
}

func TestNewWatchRendererSelection(t *testing.T) {
	cases := []struct {
		jsonOut, tty, wantJSON bool
	}{
		{true, true, true},
		{true, false, true},
		{false, false, true},
		{false, true, false},
	}
	for _, c := range cases {
		a := &app{jsonOut: c.jsonOut, streams: Streams{Out: &recWriter{}, Err: &recWriter{}, OutIsTerminal: c.tty}}
		r := a.newWatchRenderer(1)
		_, isJSON := r.(*jsonLinesRenderer)
		_, isTerm := r.(*terminalRenderer)
		if isJSON != c.wantJSON || isTerm == c.wantJSON {
			t.Fatalf("%+v: got %T", c, r)
		}
	}
}

func TestSnapshotJSON(t *testing.T) {
	type item struct {
		ID    string   `json:"id"`
		Tags  []string `json:"tags"`
		Count int      `json:"count"`
	}
	v := item{ID: "T-1", Tags: []string{"a", "b"}, Count: 3}
	got, err := snapshotJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(v)
	if !bytes.Equal(got, want) || bytes.ContainsAny(got, "\n ") {
		t.Fatalf("got %q want %q", got, want)
	}
	out := &recWriter{}
	if err := (&app{streams: Streams{Out: out}}).writeJSON(v); err != nil {
		t.Fatal(err)
	}
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out.all()), &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("%v != %v", a, b)
	}
}

func TestJSONLinesClosedOutput(t *testing.T) {
	out := &recWriter{err: fmt.Errorf("write: %w", syscall.EPIPE)}
	r := jsonApp(out, &recWriter{}).newJSONLinesRenderer()
	err := r.Update(nil, &snapshot{JSON: []byte(`{}`)})
	if !isClosedOutput(err) {
		t.Fatalf("err = %v", err)
	}
}
