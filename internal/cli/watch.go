package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/bengearig/ralph/internal/errs"
)

const (
	minWatchInterval = 100 * time.Millisecond
	watchTickPeriod  = 250 * time.Millisecond
)

type doneState int

const (
	notDone doneState = iota
	done
	stuck
)

// snapshot is what one poll produced.
type snapshot struct {
	Text  string
	JSON  []byte
	State doneState
}

func (s snapshot) equal(o snapshot) bool {
	return s.Text == o.Text && bytes.Equal(s.JSON, o.JSON)
}

type watchSource struct {
	Fetch func(ctx context.Context) (snapshot, error)
}

// watchRenderer draws snapshots; prev is nil for the first update.
type watchRenderer interface {
	Start() error
	Update(prev, cur *snapshot) error
	Tick(now time.Time) error
	Notice(message string) error
	Stop() error
}

type watchOptions struct {
	Interval  time.Duration
	UntilDone bool
}

func addWatchFlags(cmd *cobra.Command, withUntilDone bool) *watchOptions {
	opts := &watchOptions{}
	cmd.Flags().DurationVar(&opts.Interval, "interval", time.Second, "how often to poll for changes (minimum 100ms)")
	if withUntilDone {
		cmd.Flags().BoolVar(&opts.UntilDone, "until-done", false, "exit when the item is finished (exit 3 when it is stuck)")
	}
	return opts
}

func (o watchOptions) validate() error {
	if o.Interval < minWatchInterval {
		return errors.New("--interval must be at least 100ms")
	}
	return nil
}

type stuckError struct{}

func (stuckError) Error() string { return "stopped: needs a person" }
func (stuckError) Unwrap() error { return errs.ErrNeedsHuman }

func isTransientFetchError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := err.Error()
	for _, s := range []string{"database is locked", "SQLITE_BUSY", "database table is locked"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

func isClosedOutput(err error) bool {
	return errors.Is(err, syscall.EPIPE) || errors.Is(err, os.ErrClosed)
}

func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// runWatch polls src until ctx is cancelled, a fatal error occurs, the output
// closes, or (with UntilDone) the watched item finishes or gets stuck.
func (a *app) runWatch(ctx context.Context, src watchSource, opts watchOptions, r watchRenderer) (err error) {
	if err := r.Start(); err != nil {
		if isClosedOutput(err) {
			err = nil
		}
		return errors.Join(err, r.Stop())
	}
	defer func() {
		if stopErr := r.Stop(); stopErr != nil && !isClosedOutput(stopErr) {
			err = errors.Join(err, stopErr)
		}
	}()

	// render passes a renderer error through, treating a closed output as a
	// clean end of the watch.
	render := func(err error) (stop bool, ret error) {
		switch {
		case err == nil:
			return false, nil
		case isClosedOutput(err):
			return true, nil
		}
		return true, err
	}

	var prev *snapshot
	noticed := false

	// poll returns stop=true when the watch must end with ret.
	poll := func() (stop bool, ret error) {
		cur, ferr := src.Fetch(ctx)
		if ctx.Err() != nil {
			return true, errs.ErrInterrupted
		}
		if ferr != nil {
			if prev == nil || !isTransientFetchError(ferr) {
				return true, ferr
			}
			noticed = true
			return render(r.Notice("waiting: " + collapseSpaces(ferr.Error())))
		}
		if noticed {
			noticed = false
			if stop, ret := render(r.Notice("")); stop {
				return stop, ret
			}
		}
		if prev == nil || !cur.equal(*prev) {
			var p *snapshot
			if prev != nil {
				pc := *prev
				p = &pc
			}
			c := cur
			if stop, ret := render(r.Update(p, &c)); stop {
				return stop, ret
			}
		}
		prev = &cur
		if opts.UntilDone {
			switch cur.State {
			case done:
				return true, nil
			case stuck:
				return true, stuckError{}
			}
		}
		return false, nil
	}

	if stop, ret := poll(); stop {
		return ret
	}

	pollTicker := time.NewTicker(opts.Interval)
	defer pollTicker.Stop()
	tickTicker := time.NewTicker(watchTickPeriod)
	defer tickTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return errs.ErrInterrupted
		case <-pollTicker.C:
			if stop, ret := poll(); stop {
				return ret
			}
		case <-tickTicker.C:
			if stop, ret := render(r.Tick(time.Now())); stop {
				return ret
			}
		}
	}
}
