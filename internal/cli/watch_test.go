package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/errs"
)

type updateCall struct{ prev, cur *snapshot }

type fakeRenderer struct {
	mu        sync.Mutex
	starts    int
	stops     int
	ticks     int
	updates   []updateCall
	notices   []string
	updateErr error
	onUpdate  func()
}

func (f *fakeRenderer) Start() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	return nil
}

func (f *fakeRenderer) Update(prev, cur *snapshot) error {
	f.mu.Lock()
	f.updates = append(f.updates, updateCall{prev, cur})
	err, cb := f.updateErr, f.onUpdate
	f.mu.Unlock()
	if cb != nil {
		cb()
	}
	return err
}

func (f *fakeRenderer) Tick(time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ticks++
	return nil
}

func (f *fakeRenderer) Notice(m string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notices = append(f.notices, m)
	return nil
}

func (f *fakeRenderer) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	return nil
}

func (f *fakeRenderer) counts() (updates, ticks, stops int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.updates), f.ticks, f.stops
}

// scripted returns a source that yields steps in order, then repeats the last.
type step struct {
	snap snapshot
	err  error
}

func scripted(steps ...step) (watchSource, *atomic.Int64) {
	var calls atomic.Int64
	return watchSource{Fetch: func(context.Context) (snapshot, error) {
		i := int(calls.Add(1)) - 1
		if i >= len(steps) {
			i = len(steps) - 1
		}
		return steps[i].snap, steps[i].err
	}}, &calls
}

func snap(text, js string) snapshot { return snapshot{Text: text, JSON: []byte(js)} }

var fastOpts = watchOptions{Interval: 10 * time.Millisecond}

func runAsync(ctx context.Context, src watchSource, opts watchOptions, r watchRenderer) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- (&app{}).runWatch(ctx, src, opts, r) }()
	return ch
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func waitErr(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("runWatch did not return")
		return nil
	}
}

func TestWatchChangeDetection(t *testing.T) {
	src, calls := scripted(
		step{snap: snap("a", "1")},
		step{snap: snap("a", "1")},
		step{snap: snap("b", "1")},
		step{snap: snap("b", "2")},
	)
	r := &fakeRenderer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := runAsync(ctx, src, fastOpts, r)
	waitFor(t, func() bool { return calls.Load() >= 8 })
	cancel()
	if err := waitErr(t, ch); !errors.Is(err, errs.ErrInterrupted) {
		t.Fatalf("err = %v", err)
	}
	if r.starts != 1 {
		t.Fatalf("starts = %d", r.starts)
	}
	if len(r.updates) != 3 {
		t.Fatalf("updates = %d, want 3", len(r.updates))
	}
	u := r.updates
	if u[0].prev != nil || u[0].cur.Text != "a" {
		t.Fatalf("first update = %+v", u[0])
	}
	if u[1].prev.Text != "a" || u[1].cur.Text != "b" || string(u[1].cur.JSON) != "1" {
		t.Fatalf("second update wrong")
	}
	if u[2].prev.Text != "b" || string(u[2].prev.JSON) != "1" || u[2].cur.Text != "b" || string(u[2].cur.JSON) != "2" {
		t.Fatalf("json-only update wrong")
	}
	if r.stops != 1 {
		t.Fatalf("stops = %d", r.stops)
	}
}

func TestWatchTicks(t *testing.T) {
	src, _ := scripted(step{snap: snap("a", "1")})
	r := &fakeRenderer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := runAsync(ctx, src, fastOpts, r)
	waitFor(t, func() bool { _, ticks, _ := r.counts(); return ticks >= 2 })
	cancel()
	waitErr(t, ch)
}

func TestWatchTransientErrors(t *testing.T) {
	for _, terr := range []error{
		errors.New("database is locked"),
		errors.New("step: SQLITE_BUSY (5)"),
		errors.New("database table is locked"),
		fmt.Errorf("query: %w", context.DeadlineExceeded),
	} {
		t.Run(terr.Error(), func(t *testing.T) {
			src, calls := scripted(
				step{snap: snap("a", "1")},
				step{err: terr}, step{err: terr},
				step{snap: snap("a", "1")},
			)
			r := &fakeRenderer{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ch := runAsync(ctx, src, fastOpts, r)
			waitFor(t, func() bool { return calls.Load() >= 6 })
			select {
			case err := <-ch:
				t.Fatalf("watch ended: %v", err)
			default:
			}
			cancel()
			if err := waitErr(t, ch); !errors.Is(err, errs.ErrInterrupted) {
				t.Fatalf("err = %v", err)
			}
			if len(r.notices) != 3 || !strings.HasPrefix(r.notices[0], "waiting: ") || r.notices[1] != "waiting: "+collapseSpaces(terr.Error()) || r.notices[2] != "" {
				t.Fatalf("notices = %q", r.notices)
			}
			if r.stops != 1 {
				t.Fatalf("stops = %d", r.stops)
			}
		})
	}
}

func TestWatchFatalErrors(t *testing.T) {
	boom := errors.New("boom")
	t.Run("first poll", func(t *testing.T) {
		src, _ := scripted(step{err: boom})
		r := &fakeRenderer{}
		if err := (&app{}).runWatch(context.Background(), src, fastOpts, r); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		if r.stops != 1 || len(r.updates) != 0 {
			t.Fatalf("stops=%d updates=%d", r.stops, len(r.updates))
		}
	})
	t.Run("first poll transient", func(t *testing.T) {
		locked := errors.New("database is locked")
		src, _ := scripted(step{err: locked})
		r := &fakeRenderer{}
		if err := (&app{}).runWatch(context.Background(), src, fastOpts, r); !errors.Is(err, locked) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("later poll", func(t *testing.T) {
		src, _ := scripted(step{snap: snap("a", "1")}, step{err: boom})
		r := &fakeRenderer{}
		if err := (&app{}).runWatch(context.Background(), src, fastOpts, r); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		if r.stops != 1 {
			t.Fatalf("stops = %d", r.stops)
		}
	})
}

func TestWatchCancellation(t *testing.T) {
	t.Run("idle", func(t *testing.T) {
		before := runtime.NumGoroutine()
		src, _ := scripted(step{snap: snap("a", "1")})
		r := &fakeRenderer{}
		ctx, cancel := context.WithCancel(context.Background())
		ch := runAsync(ctx, src, fastOpts, r)
		waitFor(t, func() bool { u, _, _ := r.counts(); return u == 1 })
		start := time.Now()
		cancel()
		err := waitErr(t, ch)
		if !errors.Is(err, errs.ErrInterrupted) || time.Since(start) > 200*time.Millisecond {
			t.Fatalf("err = %v after %v", err, time.Since(start))
		}
		if _, _, stops := r.counts(); stops != 1 {
			t.Fatalf("stops = %d", stops)
		}
		assertGoroutines(t, before)
	})
	t.Run("in flight", func(t *testing.T) {
		before := runtime.NumGoroutine()
		var n int
		var mu sync.Mutex
		inFlight := make(chan struct{})
		src := watchSource{Fetch: func(ctx context.Context) (snapshot, error) {
			mu.Lock()
			n++
			first := n == 1
			mu.Unlock()
			if first {
				return snap("a", "1"), nil
			}
			select {
			case inFlight <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return snapshot{}, ctx.Err()
		}}
		r := &fakeRenderer{}
		ctx, cancel := context.WithCancel(context.Background())
		ch := runAsync(ctx, src, fastOpts, r)
		<-inFlight
		start := time.Now()
		cancel()
		err := waitErr(t, ch)
		if !errors.Is(err, errs.ErrInterrupted) || time.Since(start) > 200*time.Millisecond {
			t.Fatalf("err = %v after %v", err, time.Since(start))
		}
		if _, _, stops := r.counts(); stops != 1 {
			t.Fatalf("stops = %d", stops)
		}
		assertGoroutines(t, before)
	})
}

func assertGoroutines(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > want {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines = %d, want <= %d", runtime.NumGoroutine(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWatchClosedOutput(t *testing.T) {
	src, _ := scripted(step{snap: snap("a", "1")})
	for _, rerr := range []error{
		fmt.Errorf("write: %w", syscall.EPIPE),
		fmt.Errorf("write: %w", os.ErrClosed),
	} {
		r := &fakeRenderer{updateErr: rerr}
		if err := (&app{}).runWatch(context.Background(), src, fastOpts, r); err != nil {
			t.Fatalf("err = %v", err)
		}
		if r.stops != 1 {
			t.Fatalf("stops = %d", r.stops)
		}
	}
	t.Run("other error", func(t *testing.T) {
		bad := errors.New("render failed")
		r := &fakeRenderer{updateErr: bad}
		if err := (&app{}).runWatch(context.Background(), src, fastOpts, r); !errors.Is(err, bad) {
			t.Fatalf("err = %v", err)
		}
		if r.stops != 1 {
			t.Fatalf("stops = %d", r.stops)
		}
	})
}

func TestWatchUntilDone(t *testing.T) {
	withState := func(text string, st doneState) snapshot {
		s := snap(text, "1")
		s.State = st
		return s
	}
	opts := watchOptions{Interval: 10 * time.Millisecond, UntilDone: true}

	t.Run("becomes done", func(t *testing.T) {
		src, _ := scripted(step{snap: withState("a", notDone)}, step{snap: withState("b", done)})
		r := &fakeRenderer{}
		if err := (&app{}).runWatch(context.Background(), src, opts, r); err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(r.updates) != 2 || r.updates[1].cur.Text != "b" || r.stops != 1 {
			t.Fatalf("updates=%d stops=%d", len(r.updates), r.stops)
		}
	})
	t.Run("stuck", func(t *testing.T) {
		src, _ := scripted(step{snap: withState("a", notDone)}, step{snap: withState("b", stuck)})
		r := &fakeRenderer{}
		err := (&app{}).runWatch(context.Background(), src, opts, r)
		if !errors.Is(err, errs.ErrNeedsHuman) || err.Error() != "stopped: needs a person" {
			t.Fatalf("err = %v", err)
		}
		if len(r.updates) != 2 || r.stops != 1 {
			t.Fatalf("updates=%d stops=%d", len(r.updates), r.stops)
		}
	})
	t.Run("first already done", func(t *testing.T) {
		src, calls := scripted(step{snap: withState("a", done)})
		r := &fakeRenderer{}
		if err := (&app{}).runWatch(context.Background(), src, opts, r); err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(r.updates) != 1 || calls.Load() != 1 {
			t.Fatalf("updates=%d calls=%d", len(r.updates), calls.Load())
		}
	})
	t.Run("ignored without flag", func(t *testing.T) {
		src, calls := scripted(step{snap: withState("a", done)})
		r := &fakeRenderer{}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ch := runAsync(ctx, src, fastOpts, r)
		waitFor(t, func() bool { return calls.Load() >= 3 })
		cancel()
		if err := waitErr(t, ch); !errors.Is(err, errs.ErrInterrupted) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestWatchFlags(t *testing.T) {
	cmd := &cobra.Command{Use: "x"}
	opts := addWatchFlags(cmd, false)
	f := cmd.Flags().Lookup("interval")
	if f == nil || f.DefValue != "1s" {
		t.Fatalf("interval flag = %+v", f)
	}
	if cmd.Flags().Lookup("until-done") != nil {
		t.Fatal("until-done registered without request")
	}
	if opts.Interval != time.Second {
		t.Fatalf("interval = %v", opts.Interval)
	}
	cmd2 := &cobra.Command{Use: "y"}
	opts2 := addWatchFlags(cmd2, true)
	if cmd2.Flags().Lookup("until-done") == nil {
		t.Fatal("until-done missing")
	}
	if err := cmd2.ParseFlags([]string{"--until-done", "--interval", "2s"}); err != nil {
		t.Fatal(err)
	}
	if !opts2.UntilDone || opts2.Interval != 2*time.Second {
		t.Fatalf("opts = %+v", opts2)
	}
}

func TestWatchOptionsValidate(t *testing.T) {
	err := watchOptions{Interval: 50 * time.Millisecond}.validate()
	if err == nil || !strings.Contains(err.Error(), "100ms") {
		t.Fatalf("err = %v", err)
	}
	for _, d := range []time.Duration{100 * time.Millisecond, 2 * time.Second} {
		if err := (watchOptions{Interval: d}).validate(); err != nil {
			t.Fatalf("%v: %v", d, err)
		}
	}
}
