// Package version reports the build version of ralph.
package version

import (
	"fmt"
	"runtime/debug"
)

// Set via -ldflags "-X github.com/bengearig/ralph/internal/version.Version=..."
var (
	Version = "dev"
	Commit  = ""
)

// Info is the resolved build identity.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	Dirty   bool   `json:"dirty,omitempty"`
}

// Get returns the build identity, falling back to the VCS stamp embedded by
// the Go toolchain when -ldflags were not supplied.
func Get() Info {
	info := Info{Version: Version, Commit: Commit}
	if info.Commit != "" {
		return info
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			info.Commit = s.Value
		case "vcs.modified":
			info.Dirty = s.Value == "true"
		}
	}
	return info
}

func (i Info) String() string {
	s := i.Version
	if i.Commit != "" {
		c := i.Commit
		if len(c) > 12 {
			c = c[:12]
		}
		s = fmt.Sprintf("%s (%s", s, c)
		if i.Dirty {
			s += ", dirty"
		}
		s += ")"
	}
	return s
}
