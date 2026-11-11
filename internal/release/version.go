// Package release finds, downloads, verifies, and unpacks ralph releases
// published on GitHub, following the asset contract of the release pipeline.
package release

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var tagPattern = regexp.MustCompile(`^v([0-9]+)\.([0-9]+)\.([0-9]+)(?:-([0-9A-Za-z.-]+))?$`)

// Version is a parsed release tag.
type Version struct {
	Major, Minor, Patch uint64
	Suffix              string
	tag                 string
}

// ParseVersion accepts vMAJOR.MINOR.PATCH with an optional -suffix.
func ParseVersion(tag string) (Version, error) {
	m := tagPattern.FindStringSubmatch(tag)
	if m == nil {
		return Version{}, fmt.Errorf("%q is not a release tag (want vMAJOR.MINOR.PATCH with an optional -suffix)", tag)
	}
	v := Version{Suffix: m[4], tag: tag}
	for i, dst := range []*uint64{&v.Major, &v.Minor, &v.Patch} {
		n, err := strconv.ParseUint(m[i+1], 10, 64)
		if err != nil {
			return Version{}, fmt.Errorf("%q is not a release tag: number out of range", tag)
		}
		*dst = n
	}
	return v, nil
}

// String returns the original tag.
func (v Version) String() string { return v.tag }

// Compare orders versions by semver precedence and returns -1, 0, or 1.
func Compare(a, b Version) int {
	for _, p := range [][2]uint64{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if p[0] != p[1] {
			if p[0] < p[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case a.Suffix == b.Suffix:
		return 0
	case a.Suffix == "":
		return 1
	case b.Suffix == "":
		return -1
	}
	ai := strings.Split(a.Suffix, ".")
	bi := strings.Split(b.Suffix, ".")
	for i := 0; i < len(ai) && i < len(bi); i++ {
		if c := compareIdentifier(ai[i], bi[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(ai) < len(bi):
		return -1
	case len(ai) > len(bi):
		return 1
	}
	return 0
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func compareIdentifier(a, b string) int {
	an, bn := isNumeric(a), isNumeric(b)
	switch {
	case an && bn:
		a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
		if len(a) != len(b) {
			if len(a) < len(b) {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	case an:
		return -1
	case bn:
		return 1
	}
	return strings.Compare(a, b)
}
