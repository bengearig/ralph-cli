// Package errs holds sentinel errors shared across ralph's packages.
package errs

import "errors"

var (
	// ErrNotImplemented marks a capability whose interface exists but whose
	// behavior has not been built yet. The CLI maps it to a distinct exit code.
	ErrNotImplemented = errors.New("not implemented")

	// ErrNeedsHuman means work stopped because the remaining tasks need a
	// person: they are blocked, need re-articulation, or the iteration budget
	// ran out. The CLI maps it to a distinct exit code.
	ErrNeedsHuman = errors.New("needs a human")

	// ErrInterrupted means the command was ended by Ctrl-C or SIGTERM. The CLI
	// maps it to exit code 130 without printing a message.
	ErrInterrupted = errors.New("interrupted")
)
