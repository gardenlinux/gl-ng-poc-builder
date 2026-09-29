package objstore

import (
	"crypto/sha256"
	"encoding/hex"
)

// ConcatHash composes multiple input strings into a single identity hash.
// Each input string is independently SHA-256'd to 32 bytes, all digests
// are concatenated, then the concatenation is SHA-256'd to produce the
// final hash. This is the canonical mechanism for composing artifact
// identities from multiple sub-hashes.
func ConcatHash(parts ...string) Hash {
	h := sha256.New()
	for _, part := range parts {
		digest := sha256.Sum256([]byte(part))
		h.Write(digest[:])
	}
	result := hex.EncodeToString(h.Sum(nil))
	// The output of sha256 is always 32 bytes = 64 hex chars, always valid.
	return Hash{hex: result}
}
