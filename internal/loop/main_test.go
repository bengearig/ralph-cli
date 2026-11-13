package loop

import (
	"fmt"
	"os"
	"testing"
)

// TestMain gives every test one throwaway HOME and git identity, so tests need
// no per-test environment and can run in parallel.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "ralph-loop-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	env := map[string]string{
		"HOME":                home,
		"XDG_CONFIG_HOME":     home,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME":     "Ralph Test",
		"GIT_COMMITTER_NAME":  "Ralph Test",
		"GIT_AUTHOR_EMAIL":    "ralph@example.com",
		"GIT_COMMITTER_EMAIL": "ralph@example.com",
	}
	for k, v := range env {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.RemoveAll(home)
			os.Exit(1)
		}
	}
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
