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
	"articulate.md": {
		"9d2d35d971215ce6ac7bf52518c7f563a5a7f0ebde32212075e0450c2603c893",
		"706cf5c566cbb8510084e5baedd1e80a0a566206cf7aae5c0c3977a1b67a413a",
		"706437890c538b05dabefdfd3bfa8e8441425c823ced1b38bd57831d01795725",
	},
	"receive.md": {
		"82cdc25c36dfcadc00f45e836f8ccd0577e5dd940e879133648dfc4d4855525f",
		"2468e66045c06d69924ef3d17939658d2077ac7f0b165770910a802c5dc3be92",
		"324d24d9906bd861d7a4ab883be4a7e067d64648849d0e5df092a27a92a5b0bc",
		"499da570ec04c913f7004768539974a81d9c960e566a294e07e9e2e53119ec8c",
	},
	"reconcile.md": {
		"c8bfba8d98e924d00b7c98e14122f15ce3bb749f6909f3ae99ab59e4509c2841",
		"88c675052399dd7c3f5ecbe75da31ab217c00a93ab8018ea05fc7bbd5cab4b43",
	},
	"search.md": {"f31792b544902dd76bc6527c9f7e4c8cc79026d61114395afb68242f1f3fce12"},
	"status.md": {
		"4f33c088218bdecdea0ab6c011051de21bf952659e9bb38cd2eb7de717557aee",
		"804db40ea1985fa8d2d0d0fcad1c1071142700a6dc157ab037e8583e5e7bac2b",
	},
}

func commandDigest(content []byte) string {
	sum := sha256.Sum256(bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n")))
	return hex.EncodeToString(sum[:])
}

func shippedEarlier(name string, content []byte) bool {
	return slices.Contains(shippedCommands[name], commandDigest(content))
}
