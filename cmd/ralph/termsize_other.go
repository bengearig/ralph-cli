//go:build !unix

package main

func stdoutSize() (width, height int, ok bool) {
	return 0, 0, false
}
