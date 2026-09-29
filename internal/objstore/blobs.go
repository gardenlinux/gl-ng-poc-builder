package objstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Blobs provides content-addressed blob storage. Each blob is stored at a
// path derived from its SHA-256 hash: <root>/blobs/<2char>/<62char>.
// Blobs are immutable — the same hash always maps to the same content.
type Blobs struct {
	root string // path to the blobs/ directory
}

// newBlobs creates a Blobs instance rooted at the given directory.
// The directory is created if it does not exist.
func newBlobs(root string) (*Blobs, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("creating blobs directory: %w", err)
	}
	return &Blobs{root: root}, nil
}

// path returns the filesystem path for a given hash.
func (b *Blobs) path(h Hash) string {
	return filepath.Join(b.root, h.Prefix(), h.Suffix())
}

// Has reports whether a blob with the given hash exists.
func (b *Blobs) Has(h Hash) bool {
	_, err := os.Stat(b.path(h))
	return err == nil
}

// Path returns the filesystem path for a blob. This does NOT verify
// that the blob exists — use Has() for that.
func (b *Blobs) Path(h Hash) string {
	return b.path(h)
}

// Open returns an io.ReadCloser for reading the blob content.
// Returns an error if the blob does not exist.
func (b *Blobs) Open(h Hash) (io.ReadCloser, error) {
	p := b.path(h)
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("blob %s not found", h)
		}
		if errors.Is(err, os.ErrPermission) {
			// Attempt to fix permissions (may fail if not owner)
			if chErr := os.Chmod(p, 0o644); chErr == nil {
				if f, err = os.Open(p); err == nil {
					return f, nil
				}
			}
			return nil, fmt.Errorf("opening blob %s: %w (try: sudo chown $(id -u) %s)", h, err, p)
		}
		return nil, fmt.Errorf("opening blob %s: %w", h, err)
	}
	return f, nil
}

// Store reads all content from r, computes its SHA-256 hash during the write,
// and stores it as a blob. The content is first written to a temporary file,
// then atomically renamed to the final path. Returns the computed hash.
func (b *Blobs) Store(r io.Reader) (Hash, error) {
	// Create a temp file in the blobs root directory.
	tmp, err := os.CreateTemp(b.root, ".blob-tmp-*")
	if err != nil {
		return Hash{}, fmt.Errorf("creating temp file for blob: %w", err)
	}
	tmpPath := tmp.Name()

	// Ensure cleanup on any error path.
	success := false
	defer func() {
		if !success {
			tmp.Close()
			os.Remove(tmpPath)
		}
	}()

	// Write content while computing SHA-256.
	hasher := sha256.New()
	tee := io.TeeReader(r, hasher)

	if _, err := io.Copy(tmp, tee); err != nil {
		return Hash{}, fmt.Errorf("writing blob content: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return Hash{}, fmt.Errorf("closing temp blob file: %w", err)
	}

	// Compute the final hash.
	hexStr := hex.EncodeToString(hasher.Sum(nil))
	h := Hash{hex: hexStr}

	// Ensure the shard directory exists.
	shardDir := filepath.Join(b.root, h.Prefix())
	if err := os.MkdirAll(shardDir, 0o755); err != nil {
		return Hash{}, fmt.Errorf("creating shard directory: %w", err)
	}

	finalPath := b.path(h)

	// If the blob already exists, just remove the temp file and return.
	if _, err := os.Stat(finalPath); err == nil {
		os.Remove(tmpPath)
		success = true
		return h, nil
	}

	// Atomic rename from temp to final path.
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return Hash{}, fmt.Errorf("renaming blob to final path: %w", err)
	}

	// Ensure the blob is readable by the owner regardless of umask.
	os.Chmod(finalPath, 0o644)

	success = true
	return h, nil
}

// Delete removes a blob by its hash. Returns an error if the blob does not exist.
func (b *Blobs) Delete(h Hash) error {
	p := b.path(h)
	if err := os.Remove(p); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("blob %s not found", h)
		}
		return fmt.Errorf("deleting blob %s: %w", h, err)
	}
	// Try to remove the shard directory if empty (best effort).
	os.Remove(filepath.Join(b.root, h.Prefix()))
	return nil
}

// Sweep deletes every blob not present in keep. It applies no policy of its
// own — the caller computes the keep-set (graph reachability + pins). Returns
// the number of blobs deleted.
func (b *Blobs) Sweep(keep map[Hash]struct{}) (int, error) {
	var toDelete []Hash
	err := b.Iterate(func(h Hash) error {
		if _, ok := keep[h]; !ok {
			toDelete = append(toDelete, h)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, h := range toDelete {
		if err := b.Delete(h); err != nil {
			return deleted, fmt.Errorf("sweep: %w", err)
		}
		deleted++
	}
	return deleted, nil
}

// Iterate calls fn for each blob hash in the store. If fn returns an error,
// iteration stops and that error is returned.
func (b *Blobs) Iterate(fn func(Hash) error) error {
	shards, err := os.ReadDir(b.root)
	if err != nil {
		return fmt.Errorf("reading blobs directory: %w", err)
	}

	for _, shard := range shards {
		if !shard.IsDir() || len(shard.Name()) != 2 {
			continue
		}
		shardPath := filepath.Join(b.root, shard.Name())
		entries, err := os.ReadDir(shardPath)
		if err != nil {
			return fmt.Errorf("reading shard directory %s: %w", shard.Name(), err)
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
