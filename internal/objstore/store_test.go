package objstore

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStore_Open(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if store.Root() != dir {
		t.Errorf("Root() = %q, want %q", store.Root(), dir)
	}
	if store.Blobs == nil {
		t.Error("Blobs should not be nil")
	}
	if store.Map == nil {
		t.Error("Map should not be nil")
	}
	if store.Pins == nil {
		t.Error("Pins should not be nil")
	}

	// Verify directories were created.
	blobsDir := filepath.Join(dir, "blobs")
	if _, err := os.Stat(blobsDir); err != nil {
		t.Errorf("blobs directory not created: %v", err)
	}
	mapDir := filepath.Join(dir, "map")
	if _, err := os.Stat(mapDir); err != nil {
		t.Errorf("map directory not created: %v", err)
	}
}

func TestStore_OpenCreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "store")
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if store.Root() != dir {
		t.Errorf("Root() = %q, want %q", store.Root(), dir)
	}
}

func TestStore_OpenEmptyUsesDefault(t *testing.T) {
	// Set GL_CACHE to a temp dir so we don't pollute ~/.cache.
	dir := t.TempDir()
	t.Setenv("GL_CACHE", dir)

	store, err := Open("")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if store.Root() != dir {
		t.Errorf("Root() = %q, want %q (from GL_CACHE)", store.Root(), dir)
	}
}

func TestStore_GLCacheEnvVar(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GL_CACHE", dir)

	root := DefaultRoot()
	if root != dir {
		t.Errorf("DefaultRoot() = %q, want %q", root, dir)
	}
}

func TestStore_DefaultRootWithoutEnv(t *testing.T) {
	t.Setenv("GL_CACHE", "")

	root := DefaultRoot()
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".cache", "gl-ng")
	if root != expected {
		t.Errorf("DefaultRoot() = %q, want %q", root, expected)
	}
}

func TestStore_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Store a blob.
	content := []byte("end-to-end test content")
	blobHash, err := store.Blobs.Store(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Blobs.Store: %v", err)
	}

	// Create a map entry pointing to it.
	key := ConcatHash("my-artifact", "amd64")
	err = store.Map.Set(key, blobHash, true)
	if err != nil {
		t.Fatalf("Map.Set: %v", err)
	}

	// Verify the mapping.
	got, err := store.Map.Get(key)
	if err != nil {
		t.Fatalf("Map.Get: %v", err)
	}
	if !got.Equal(blobHash) {
		t.Errorf("Map.Get = %s, want %s", got, blobHash)
	}

	// Verify blob content.
	rc, err := store.Blobs.Open(got)
	if err != nil {
		t.Fatalf("Blobs.Open: %v", err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	if !bytes.Equal(data, content) {
		t.Errorf("content = %q, want %q", data, content)
	}

	// Pin the blob.
	pinID, err := store.Pins.Create("test source blob", []Hash{blobHash})
	if err != nil {
		t.Fatalf("Pins.Create: %v", err)
	}
	if _, ok := store.Pins.ReachableBlobs()[blobHash]; !ok {
		t.Error("blob should be protected by a pin")
	}
	if _, err := store.Pins.Get(pinID); err != nil {
		t.Errorf("Pins.Get: %v", err)
	}
}

func TestStore_MapValidation(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	key := MustHash("1111111111111111111111111111111111111111111111111111111111111111")
	nonExistentBlob := MustHash("2222222222222222222222222222222222222222222222222222222222222222")

	// With validation, should fail.
	err = store.Map.Set(key, nonExistentBlob, true)
	if err == nil {
		t.Error("Map.Set with validation should fail for non-existent blob")
	}
	if !strings.Contains(err.Error(), "does not reference an existing blob") {
		t.Errorf("unexpected error: %v", err)
	}

	// Without validation, should succeed.
	err = store.Map.Set(key, nonExistentBlob, false)
	if err != nil {
		t.Fatalf("Map.Set without validation: %v", err)
	}
}

func TestStore_Reopen(t *testing.T) {
	dir := t.TempDir()

	// First session: create data.
	store1, err := Open(dir)
	if err != nil {
		t.Fatalf("Open 1: %v", err)
	}

	content := []byte("persistent data")
	blobHash, _ := store1.Blobs.Store(bytes.NewReader(content))
	key := MustHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1111")
	store1.Map.Set(key, blobHash, false)
	store1.Pins.Create("reopened source", []Hash{blobHash})

	// Second session: verify data persists.
	store2, err := Open(dir)
	if err != nil {
		t.Fatalf("Open 2: %v", err)
	}

	if !store2.Blobs.Has(blobHash) {
		t.Error("blob should persist across reopen")
	}
	got, err := store2.Map.Get(key)
	if err != nil {
		t.Fatalf("Map.Get after reopen: %v", err)
	}
	if !got.Equal(blobHash) {
		t.Errorf("Map.Get = %s, want %s", got, blobHash)
	}
	if _, ok := store2.Pins.ReachableBlobs()[blobHash]; !ok {
		t.Error("pin protection should persist across reopen")
	}
}
