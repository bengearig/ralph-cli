package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mattn/go-isatty"

	"github.com/bengearig/ralph/internal/cli"
	"github.com/bengearig/ralph/internal/errs"
)

const (
	exitError          = 1
	exitNotImplemented = 2
	exitNeedsHuman     = 3
	exitInterrupted    = 130
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := cli.Execute(ctx, cli.Streams{
		In:            os.Stdin,
		Out:           os.Stdout,
		Err:           os.Stderr,
		IsTerminal:    isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd()),
		ErrIsTerminal: isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd()),
		OutIsTerminal: isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd()),
		OutSize:       stdoutSize,
	}, os.Args[1:])
	stop()
	code, print := exitCode(err)
	if print {
		fmt.Fprintln(os.Stderr, "ralph:", err)
	}
	os.Exit(code)
}

// exitCode maps a command error to a process exit code and whether to print it.
func exitCode(err error) (code int, print bool) {
	switch {
	case err == nil:
		return 0, false
	case errors.Is(err, errs.ErrInterrupted):
		return exitInterrupted, false
	case errors.Is(err, errs.ErrNotImplemented):
		return exitNotImplemented, true
	case errors.Is(err, errs.ErrNeedsHuman):
		return exitNeedsHuman, true
	}
	return exitError, true
}
