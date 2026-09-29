// Package objstore implements the object store for the gl-ng build system.
// It provides content-addressed blob storage, identity-based map lookups,
// and source classification for garbage collection.
package objstore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
)

// CookieKey computes a deterministic map key for caching an InRelease file
// identified by a cookie string, repository URL, and distribution.
func CookieKey(cookie, repoURL, dist string) Hash {
	h := sha256.Sum256([]byte("InRelease:" + repoURL + ":" + dist + ":" + cookie))
	return MustHash(hex.EncodeToString(h[:]))
}

// hashPattern matches exactly 64 lowercase hex characters.
var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Hash is a validated SHA-256 hash value. Once constructed, the value is
// guaranteed to be exactly 64 lowercase hexadecimal characters. The zero
// value is not valid — always use NewHash or MustHash to construct.
type Hash struct {
	hex string
}

// NewHash constructs a Hash from a hex string, returning an error if the
// input is not exactly 64 lowercase hex characters.
func NewHash(s string) (Hash, error) {
	if !hashPattern.MatchString(s) {
		return Hash{}, fmt.Errorf("invalid hash %q: must be exactly 64 lowercase hex characters", s)
	}
	return Hash{hex: s}, nil
}

// MustHash constructs a Hash from a hex string, panicking if invalid.
// Use only in tests or for compile-time constants.
func MustHash(s string) Hash {
	h, err := NewHash(s)
	if err != nil {
		panic(err)
	}
	return h
}

// String returns the 64-character hex representation of the hash.
func (h Hash) String() string {
	return h.hex
}

// Short returns the first 12 characters of the hash for display purposes.
func (h Hash) Short() string {
	if len(h.hex) >= 12 {
		return h.hex[:12]
	}
	return h.hex
}

// IsZero reports whether the hash is the zero value (uninitialized).
func (h Hash) IsZero() bool {
	return h.hex == ""
}

// Equal reports whether two hashes are equal.
func (h Hash) Equal(other Hash) bool {
	return h.hex == other.hex
}

// Prefix returns the first 2 characters of the hash (used for directory sharding).
func (h Hash) Prefix() string {
	return h.hex[:2]
}

// Suffix returns characters 3-64 of the hash (used for the filename within the shard directory).
func (h Hash) Suffix() string {
	return h.hex[2:]
}
