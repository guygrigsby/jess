package ledger

import (
	"crypto/sha256"
	"encoding/hex"
)

// hashOf returns the sha256 hex of b, empty for nil/empty.
func hashOf(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
