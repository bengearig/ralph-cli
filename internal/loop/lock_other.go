//go:build !unix && !windows

package loop

import "os"

// Platforms without advisory locks run unguarded.
func lockFile(*os.File) error   { return nil }
func unlockFile(*os.File) error { return nil }
