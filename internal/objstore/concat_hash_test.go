package objstore

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestConcatHash_SinglePart(t *testing.T) {
	// ConcatHash with a single part should be SHA256(SHA256(part))
	part := "hello"
	inner := sha256.Sum256([]byte(part))
	outer := sha256.Sum256(inner[:])
	expected := hex.EncodeToString(outer[:])

	result := ConcatHash(part)
	if result.String() != expected {
		t.Errorf("ConcatHash(%q) = %s, want %s", part, result, expected)
	}
}

func TestConcatHash_MultipleParts(t *testing.T) {
	// ConcatHash("a", "b") should be SHA256(SHA256("a") || SHA256("b"))
	da := sha256.Sum256([]byte("a"))
	db := sha256.Sum256([]byte("b"))
	combined := append(da[:], db[:]...)
	outer := sha256.Sum256(combined)
	expected := hex.EncodeToString(outer[:])

	result := ConcatHash("a", "b")
	if result.String() != expected {
		t.Errorf("ConcatHash(\"a\", \"b\") = %s, want %s", result, expected)
	}
}

func TestConcatHash_Empty(t *testing.T) {
	// ConcatHash with no parts should be SHA256 of empty input.
	outer := sha256.Sum256(nil)
	expected := hex.EncodeToString(outer[:])

	result := ConcatHash()
	if result.String() != expected {
		t.Errorf("ConcatHash() = %s, want %s", result, expected)
	}
}

func TestConcatHash_OrderMatters(t *testing.T) {
	ab := ConcatHash("a", "b")
	ba := ConcatHash("b", "a")
	if ab.Equal(ba) {
		t.Error("ConcatHash should be order-dependent")
	}
}

func TestConcatHash_DifferentInputsDifferentOutput(t *testing.T) {
	h1 := ConcatHash("input1")
	h2 := ConcatHash("input2")
	if h1.Equal(h2) {
		t.Error("different inputs should produce different hashes")
	}
}

func TestConcatHash_Deterministic(t *testing.T) {
	h1 := ConcatHash("a", "b", "c")
	h2 := ConcatHash("a", "b", "c")
	if !h1.Equal(h2) {
		t.Error("same inputs should produce same hash")
	}
}

func TestConcatHash_ResultIsValidHash(t *testing.T) {
	h := ConcatHash("test")
	// Verify the result is a valid Hash (64 lowercase hex chars).
	_, err := NewHash(h.String())
	if err != nil {
		t.Errorf("ConcatHash result is not a valid hash: %v", err)
	}
}
