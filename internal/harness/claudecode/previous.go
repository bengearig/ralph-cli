package claudecode

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"slices"
)

// shippedCommands holds SHA-256 digests of earlier versions of each slash
// command template, so reinstalling replaces copies nobody edited. When a
// template changes, add the digest of the version being replaced.
var shippedCommands = map[string][]string{
	"articulate.md": {"9d2d35d971215ce6ac7bf52518c7f563a5a7f0ebde32212075e0450c2603c893"},
	"receive.md":    {"82cdc25c36dfcadc00f45e836f8ccd0577e5dd940e879133648dfc4d4855525f"},
	"status.md":     {"4f33c088218bdecdea0ab6c011051de21bf952659e9bb38cd2eb7de717557aee"},
}

func commandDigest(content []byte) string {
	sum := sha256.Sum256(bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n")))
	return hex.EncodeToString(sum[:])
}

func shippedEarlier(name string, content []byte) bool {
	return slices.Contains(shippedCommands[name], commandDigest(content))
}
