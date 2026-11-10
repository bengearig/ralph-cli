package workflow

import (
	"fmt"
	"strings"
)

const headerFence = "---"

var headerKeys = []string{"description", "argument", "argument-hint", "argument-description"}

// parseSource splits a source into its metadata header and template body.
// The header holds exactly the keys in headerKeys, each once, one per line.
// A single blank line after the closing fence is not part of the body.
func parseSource(name, raw string) (Workflow, string, error) {
	w := Workflow{Name: name}
	lines := strings.Split(raw, "\n")
	if lines[0] != headerFence {
		return w, "", fmt.Errorf("source must start with a %q line", headerFence)
	}
	vals := map[string]string{}
	end := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == headerFence {
			end = i
			break
		}
		key, val, ok := strings.Cut(lines[i], ": ")
		if !ok {
			return w, "", fmt.Errorf("header line %d: want \"key: value\"", i+1)
		}
		if !isHeaderKey(key) {
			return w, "", fmt.Errorf("header line %d: unknown key %q", i+1, key)
		}
		if _, dup := vals[key]; dup {
			return w, "", fmt.Errorf("header line %d: duplicate key %q", i+1, key)
		}
		if strings.TrimSpace(val) == "" {
			return w, "", fmt.Errorf("header line %d: empty value for %q", i+1, key)
		}
		vals[key] = val
	}
	if end < 0 {
		return w, "", fmt.Errorf("header is not closed by a %q line", headerFence)
	}
	for _, k := range headerKeys {
		if _, ok := vals[k]; !ok {
			return w, "", fmt.Errorf("header is missing key %q", k)
		}
	}
	w.Description = vals["description"]
	w.Argument = vals["argument"]
	w.ArgumentHint = vals["argument-hint"]
	w.ArgumentDescription = vals["argument-description"]
	rest := lines[end+1:]
	if len(rest) > 0 && rest[0] == "" {
		rest = rest[1:]
	}
	return w, strings.Join(rest, "\n"), nil
}

func isHeaderKey(k string) bool {
	for _, h := range headerKeys {
		if h == k {
			return true
		}
	}
	return false
}
