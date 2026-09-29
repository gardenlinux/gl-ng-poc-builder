package dirhash

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// computeExpectedDirHash manually computes the expected hash for a directory tree.
// This mirrors the algorithm exactly to serve as a test oracle.
func computeExpectedDirHash(t *testing.T, path string) string {
	t.Helper()
	h := sha256.New()

	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("ReadDir %q: %v", path, err)
	}

	for _, entry := range entries {
		name := entry.Name()
		full := filepath.Join(path, name)

		info, err := os.Lstat(full)
		if err != nil {
			t.Fatalf("Lstat %q: %v", full, err)
		}

		var typeByte byte
		var contentHash [32]byte

		mode := info.Mode()
		switch {
		case mode.IsRegular():
			typeByte = TypeRegular
			if mode&0100 != 0 {
				typeByte = TypeExecutable
			}
			data, err := os.ReadFile(full)
			if err != nil {
				t.Fatalf("ReadFile %q: %v", full, err)
			}
			contentHash = sha256.Sum256(data)

		case mode.IsDir():
			typeByte = TypeDirectory
			subHash := computeExpectedDirHash(t, full)
			decoded, _ := hex.DecodeString(subHash)
			copy(contentHash[:], decoded)

		case mode&os.ModeSymlink != 0:
			typeByte = TypeSymlink
			target, err := os.Readlink(full)
			if err != nil {
				t.Fatalf("Readlink %q: %v", full, err)
			}
			contentHash = sha256.Sum256([]byte(target))

		default:
			typeByte = TypeSpecial
			contentHash = sha256.Sum256([]byte(mode.Type().String()))
		}

		h.Write([]byte{typeByte})
		nameDigest := sha256.Sum256([]byte(name))
		h.Write(nameDigest[:])
		h.Write(contentHash[:])
	}

	return hex.EncodeToString(h.Sum(nil))
}

func TestHashDirectory_EmptyDir(t *testing.T) {
	dir := t.TempDir()

	hash, err := HashDirectory(dir)
	if err != nil {
		t.Fatalf("HashDirectory: %v", err)
	}

	// Empty directory: SHA-256 of empty input
	expected := hex.EncodeToString(sha256.New().Sum(nil))
	if hash != expected {
		t.Errorf("empty dir hash = %s, want %s", hash, expected)
	}
}

func TestHashDirectory_SingleFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world\n"), 0644); err != nil {
		t.Fatal(err)
	}

	hash, err := HashDirectory(dir)
	if err != nil {
		t.Fatalf("HashDirectory: %v", err)
	}

	expected := computeExpectedDirHash(t, dir)
	if hash != expected {
		t.Errorf("hash = %s, want %s", hash, expected)
	}

	// Verify it's a valid 64-char hex string
	if len(hash) != 64 {
		t.Errorf("hash length = %d, want 64", len(hash))
	}
}

func TestHashDirectory_Deterministic(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("aaa"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("bbb"), 0644); err != nil {
		t.Fatal(err)
	}

	hash1, err := HashDirectory(dir)
	if err != nil {
		t.Fatalf("HashDirectory (1st): %v", err)
	}

	hash2, err := HashDirectory(dir)
	if err != nil {
		t.Fatalf("HashDirectory (2nd): %v", err)
	}

	if hash1 != hash2 {
		t.Errorf("non-deterministic: %s != %s", hash1, hash2)
	}
}

func TestHashDirectory_ExecutableVsNonExecutable(t *testing.T) {
	// Non-executable file
	dir1 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir1, "script"), []byte("#!/bin/sh\necho hi\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Executable file with same content
	dir2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir2, "script"), []byte("#!/bin/sh\necho hi\n"), 0755); err != nil {
		t.Fatal(err)
	}

	hash1, err := HashDirectory(dir1)
	if err != nil {
		t.Fatalf("HashDirectory (non-exec): %v", err)
	}

	hash2, err := HashDirectory(dir2)
	if err != nil {
		t.Fatalf("HashDirectory (exec): %v", err)
	}

	if hash1 == hash2 {
		t.Error("executable and non-executable files should produce different hashes")
	}
}

func TestHashDirectory_Symlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "target.txt"), []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}

	hash, err := HashDirectory(dir)
	if err != nil {
		t.Fatalf("HashDirectory: %v", err)
	}

	expected := computeExpectedDirHash(t, dir)
	if hash != expected {
		t.Errorf("hash = %s, want %s", hash, expected)
	}
}

func TestHashDirectory_SymlinkTargetMatters(t *testing.T) {
	// Two dirs with same file content but symlink points to different targets
	dir1 := t.TempDir()
	if err := os.Symlink("target_a", filepath.Join(dir1, "link")); err != nil {
		t.Fatal(err)
	}

	dir2 := t.TempDir()
	if err := os.Symlink("target_b", filepath.Join(dir2, "link")); err != nil {
		t.Fatal(err)
	}

	hash1, err := HashDirectory(dir1)
	if err != nil {
		t.Fatalf("HashDirectory (link to target_a): %v", err)
	}

	hash2, err := HashDirectory(dir2)
	if err != nil {
		t.Fatalf("HashDirectory (link to target_b): %v", err)
	}

	if hash1 == hash2 {
		t.Error("different symlink targets should produce different hashes")
	}
}

func TestHashDirectory_NestedDirectories(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "inner.txt"), []byte("inner content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "outer.txt"), []byte("outer content"), 0644); err != nil {
		t.Fatal(err)
	}

	hash, err := HashDirectory(dir)
	if err != nil {
		t.Fatalf("HashDirectory: %v", err)
	}

	expected := computeExpectedDirHash(t, dir)
	if hash != expected {
		t.Errorf("hash = %s, want %s", hash, expected)
	}
}

func TestHashDirectory_DeeplyNested(t *testing.T) {
	dir := t.TempDir()
	deep := filepath.Join(dir, "a", "b", "c", "d")
	if err := os.MkdirAll(deep, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "file.txt"), []byte("deep"), 0644); err != nil {
		t.Fatal(err)
	}

	hash, err := HashDirectory(dir)
	if err != nil {
		t.Fatalf("HashDirectory: %v", err)
	}

	expected := computeExpectedDirHash(t, dir)
	if hash != expected {
		t.Errorf("hash = %s, want %s", hash, expected)
	}
}

func TestHashDirectory_ContentChangeAffectsHash(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "data.txt")
	if err := os.WriteFile(file, []byte("version 1"), 0644); err != nil {
		t.Fatal(err)
	}

	hash1, err := HashDirectory(dir)
	if err != nil {
		t.Fatalf("HashDirectory (v1): %v", err)
	}

	if err := os.WriteFile(file, []byte("version 2"), 0644); err != nil {
		t.Fatal(err)
	}

	hash2, err := HashDirectory(dir)
	if err != nil {
		t.Fatalf("HashDirectory (v2): %v", err)
	}

	if hash1 == hash2 {
		t.Error("different file content should produce different hashes")
	}
}

func TestHashDirectory_FileNameChangeAffectsHash(t *testing.T) {
	dir1 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir1, "alpha.txt"), []byte("same content"), 0644); err != nil {
		t.Fatal(err)
	}

	dir2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir2, "beta.txt"), []byte("same content"), 0644); err != nil {
		t.Fatal(err)
	}

	hash1, err := HashDirectory(dir1)
	if err != nil {
		t.Fatalf("HashDirectory (alpha): %v", err)
	}

	hash2, err := HashDirectory(dir2)
	if err != nil {
		t.Fatalf("HashDirectory (beta): %v", err)
	}

	if hash1 == hash2 {
		t.Error("different file names should produce different hashes")
	}
}

func TestHashDirectory_SortOrder(t *testing.T) {
	// Create files in non-alphabetical order; hash should be independent of creation order
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "z_last.txt"), []byte("z"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a_first.txt"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "m_middle.txt"), []byte("m"), 0644); err != nil {
		t.Fatal(err)
	}

	hash, err := HashDirectory(dir)
	if err != nil {
		t.Fatalf("HashDirectory: %v", err)
	}

	expected := computeExpectedDirHash(t, dir)
	if hash != expected {
		t.Errorf("hash = %s, want %s", hash, expected)
	}
}

func TestHashDirectory_SpecialFile(t *testing.T) {
	// FIFOs / sockets / device nodes are recorded by name and a hash of their
	// mode type. They aren't opened (would block / fail) but their presence
	// and kind still affect the directory hash.
	dir := t.TempDir()
	fifoPath := filepath.Join(dir, "myfifo")

	if err := syscall.Mkfifo(fifoPath, 0644); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}

	hash1, err := HashDirectory(dir)
	if err != nil {
		t.Fatalf("HashDirectory with FIFO: %v", err)
	}

	hash2, err := HashDirectory(dir)
	if err != nil {
		t.Fatalf("HashDirectory with FIFO (second call): %v", err)
	}
	if hash1 != hash2 {
		t.Errorf("FIFO hash not deterministic: %s vs %s", hash1, hash2)
	}

	expected := computeExpectedDirHash(t, dir)
	if hash1 != expected {
		t.Errorf("hash = %s, want %s", hash1, expected)
	}

	if err := os.Remove(fifoPath); err != nil {
		t.Fatalf("Remove fifo: %v", err)
	}
	hash3, err := HashDirectory(dir)
	if err != nil {
		t.Fatalf("HashDirectory after fifo removed: %v", err)
	}
	if hash1 == hash3 {
		t.Errorf("hash unchanged after FIFO removal: %s", hash1)
	}
}

func TestHashDirectory_NonexistentPath(t *testing.T) {
	_, err := HashDirectory("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Fatal("expected error for nonexistent path, got nil")
	}
}

func TestHashDirectory_MixedContent(t *testing.T) {
	// Complex tree with all supported types
	dir := t.TempDir()

	// Regular file
	if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# Hello"), 0644); err != nil {
		t.Fatal(err)
	}

	// Executable file
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\necho hi"), 0755); err != nil {
		t.Fatal(err)
	}

	// Subdirectory with content
	sub := filepath.Join(dir, "src")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "main.go"), []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}

	// Symlink
	if err := os.Symlink("readme.md", filepath.Join(dir, "docs")); err != nil {
		t.Fatal(err)
	}

	hash, err := HashDirectory(dir)
	if err != nil {
		t.Fatalf("HashDirectory: %v", err)
	}

	expected := computeExpectedDirHash(t, dir)
	if hash != expected {
		t.Errorf("hash = %s, want %s", hash, expected)
	}
}

func TestHashDirectory_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "empty"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	hash, err := HashDirectory(dir)
	if err != nil {
		t.Fatalf("HashDirectory: %v", err)
	}

	expected := computeExpectedDirHash(t, dir)
	if hash != expected {
		t.Errorf("hash = %s, want %s", hash, expected)
	}
}

func TestHashDirectory_EmptySubdir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "empty_sub"), 0755); err != nil {
		t.Fatal(err)
	}

	hash, err := HashDirectory(dir)
	if err != nil {
		t.Fatalf("HashDirectory: %v", err)
	}

	expected := computeExpectedDirHash(t, dir)
	if hash != expected {
		t.Errorf("hash = %s, want %s", hash, expected)
	}
}

// contains is a helper to check substring membership without importing strings in tests.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
