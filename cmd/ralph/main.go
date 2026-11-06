package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/bengearig/ralph/internal/cli"
	"github.com/bengearig/ralph/internal/errs"
)

const (
	exitError          = 1
	exitNotImplemented = 2
	exitNeedsHuman     = 3
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := cli.Execute(ctx, cli.Streams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}, os.Args[1:])
	stop()
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "ralph:", err)
	switch {
	case errors.Is(err, errs.ErrNotImplemented):
		os.Exit(exitNotImplemented)
	case errors.Is(err, errs.ErrNeedsHuman):
		os.Exit(exitNeedsHuman)
	}
	os.Exit(exitError)
}
