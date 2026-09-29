package objstore

import (
	"fmt"
	"os"
	"path/filepath"
)

// DefaultRoot returns the default object store root directory.
// This is ~/.cache/gl-ng unless overridden by the GL_CACHE environment variable.
func DefaultRoot() string {
	if env := os.Getenv("GL_CACHE"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// Fallback if home directory cannot be determined.
		return filepath.Join(os.TempDir(), "gl-ng-cache")
	}
	return filepath.Join(home, ".cache", "gl-ng")
}

// Store is the top-level object store combining blobs, map, and pin
// protection. It is the primary storage abstraction for the build system.
type Store struct {
	root  string
	Blobs *Blobs
	Map   *MapStore
	Pins  *Pins
}

// Open opens (or creates) an object store at the given root directory.
// If root is empty, DefaultRoot() is used.
func Open(root string) (*Store, error) {
	if root == "" {
		root = DefaultRoot()
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolving absolute path for %s: %w", root, err)
	}
	root = absRoot

	// Ensure root exists.
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("creating store root %s: %w", root, err)
	}

	blobsDir := filepath.Join(root, "blobs")
	blobs, err := newBlobs(blobsDir)
	if err != nil {
		return nil, fmt.Errorf("initializing blobs: %w", err)
	}

	mapDir := filepath.Join(root, "map")
	mapStore, err := newMapStore(mapDir, blobs)
	if err != nil {
		return nil, fmt.Errorf("initializing map: %w", err)
	}

	pinsDir := filepath.Join(root, "pins")
	pins, err := newPins(pinsDir)
	if err != nil {
		return nil, fmt.Errorf("initializing pins: %w", err)
	}

	return &Store{
		root:  root,
		Blobs: blobs,
		Map:   mapStore,
		Pins:  pins,
	}, nil
}

// Root returns the filesystem root of the store.
func (s *Store) Root() string {
	return s.root
}
