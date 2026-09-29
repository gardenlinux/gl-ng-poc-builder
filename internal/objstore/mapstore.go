package objstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MapStore provides identity-based hash lookups. Each map entry is a file
// at <root>/map/<2char>/<62char> containing a single line: the blob hash
// that the identity maps to.
type MapStore struct {
	root  string // path to the map/ directory
	blobs *Blobs // reference to blobs for validation
}

// newMapStore creates a MapStore instance rooted at the given directory.
// The directory is created if it does not exist.
func newMapStore(root string, blobs *Blobs) (*MapStore, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("creating map directory: %w", err)
	}
	return &MapStore{root: root, blobs: blobs}, nil
}

// path returns the filesystem path for a given key hash.
func (m *MapStore) path(key Hash) string {
	return filepath.Join(m.root, key.Prefix(), key.Suffix())
}

// Has reports whether a mapping exists for the given key.
func (m *MapStore) Has(key Hash) bool {
	_, err := os.Stat(m.path(key))
	return err == nil
}

// Get retrieves the blob hash that the given key maps to.
// Returns an error if the mapping does not exist.
func (m *MapStore) Get(key Hash) (Hash, error) {
	data, err := os.ReadFile(m.path(key))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Hash{}, fmt.Errorf("map key %s not found", key)
		}
		return Hash{}, fmt.Errorf("reading map entry %s: %w", key, err)
	}
	hexStr := strings.TrimSpace(string(data))
	h, err := NewHash(hexStr)
	if err != nil {
		return Hash{}, fmt.Errorf("invalid hash in map entry %s: %w", key, err)
	}
	return h, nil
}

// Set creates or updates a mapping from key to value (a blob hash).
// If validate is true, it checks that the value references an existing blob.
func (m *MapStore) Set(key, value Hash, validate bool) error {
	if validate && !m.blobs.Has(value) {
		return fmt.Errorf("map value %s does not reference an existing blob", value)
	}

	// Ensure the shard directory exists.
	shardDir := filepath.Join(m.root, key.Prefix())
	if err := os.MkdirAll(shardDir, 0o755); err != nil {
		return fmt.Errorf("creating map shard directory: %w", err)
	}

	// Write atomically via temp+rename.
	tmp, err := os.CreateTemp(shardDir, ".map-tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file for map entry: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := fmt.Fprintf(tmp, "%s\n", value); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("writing map entry: %w", err)
	}

	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("closing temp map file: %w", err)
	}

	if err := os.Rename(tmpPath, m.path(key)); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("renaming map entry to final path: %w", err)
	}

	return nil
}

// Delete removes a mapping by its key. Returns an error if the mapping does not exist.
func (m *MapStore) Delete(key Hash) error {
	p := m.path(key)
	if err := os.Remove(p); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("map key %s not found", key)
		}
		return fmt.Errorf("deleting map entry %s: %w", key, err)
	}
	// Try to remove the shard directory if empty (best effort).
	os.Remove(filepath.Join(m.root, key.Prefix()))
	return nil
}

// Iterate calls fn for each key in the map store. If fn returns an error,
// iteration stops and that error is returned.
func (m *MapStore) Iterate(fn func(key Hash) error) error {
	shards, err := os.ReadDir(m.root)
	if err != nil {
		return fmt.Errorf("reading map directory: %w", err)
	}

	for _, shard := range shards {
		if !shard.IsDir() || len(shard.Name()) != 2 {
			continue
		}
		shardPath := filepath.Join(m.root, shard.Name())
		entries, err := os.ReadDir(shardPath)
		if err != nil {
			return fmt.Errorf("reading map shard directory %s: %w", shard.Name(), err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			hexStr := shard.Name() + entry.Name()
			h, err := NewHash(hexStr)
			if err != nil {
				// Skip invalid filenames (e.g., temp files).
				continue
			}
			if err := fn(h); err != nil {
				return err
			}
		}
	}
	return nil
}
