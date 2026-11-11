//go:build unix

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

func stdoutSize() (width, height int, ok bool) {
	ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 || ws.Row == 0 {
		return 0, 0, false
	}
	return int(ws.Col), int(ws.Row), true
}
