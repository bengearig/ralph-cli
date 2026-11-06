// Package errs holds sentinel errors shared across ralph's packages.
package errs

import "errors"

// ErrNotImplemented marks a capability whose interface exists but whose
// behavior has not been built yet. The CLI maps it to a distinct exit code.
var ErrNotImplemented = errors.New("not implemented")
