package objstore

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

// Remote is the pull-through backend: an OCI registry the store falls through
// to on a local miss. A nil remote means the store is pure-local (current
// behavior). The concrete implementation lives in internal/ociclient; the
// interface keeps objstore free of any OCI/HTTP dependency. See
// oci-cache-design.md §12.2.
type Remote interface {
	// PullBlobByDigest fetches a blob by its content hash. It must return an
	// error for which errors.Is(err, os.ErrNotExist) or a documented sentinel
	// holds when the blob is absent remotely; the caller treats any error as a
	// miss and surfaces the original local not-found.
	PullBlobByDigest(h Hash) (io.ReadCloser, int64, error)
	// PullOutputManifest fetches the output manifest for an artifact identity,
	// returning its leaf outputs. ok=false means the tag is absent remotely.
	PullOutputManifest(identity Hash) (leaves []Output, ok bool, err error)
}

// Store is the top-level object store combining blobs, map, and pin
// protection. It is the primary storage abstraction for the build system.
// An optional Remote turns reads into a pull-through cache.
type Store struct {
	root   string
	Blobs  *Blobs
	Map    *MapStore
	Pins   *Pins
	remote Remote
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

// SetRemote attaches (or clears, with nil) a pull-through remote. It is set by
// cmd/gl from GL_REGISTRY before the read paths run; the cache-admin commands
// leave it nil so they show local truth only.
func (s *Store) SetRemote(r Remote) {
	s.remote = r
}

// HasRemote reports whether a pull-through remote is configured.
func (s *Store) HasRemote() bool {
	return s.remote != nil
}

// OpenBlob opens a blob, falling through to the remote by digest on a local
// miss (oci-cache-design.md §11.6). A pulled blob is re-hashed on write, which
// verifies its digest for free. With no remote this is exactly Blobs.Open.
func (s *Store) OpenBlob(h Hash) (io.ReadCloser, error) {
	if s.Blobs.Has(h) || s.remote == nil {
		return s.Blobs.Open(h)
	}
	if err := s.pullBlob(h); err != nil {
		// Fall through to the local open, which yields the canonical
		// not-found error.
		return s.Blobs.Open(h)
	}
	return s.Blobs.Open(h)
}

// EnsureBlob materializes a blob into the local store if absent, pulling by
// digest from the remote. It is a no-op if the blob is present or no remote is
// configured. Callers that consume a blob by filesystem path (bind-mount,
// dpkg-deb, os.Open(Blobs.Path)) — rootfs assembly and .deb install — call
// this at their Blobs.Has guard so those paths fall through too (§12.2 gap).
func (s *Store) EnsureBlob(h Hash) error {
	if s.Blobs.Has(h) || s.remote == nil {
		return nil
	}
	return s.pullBlob(h)
}

// pullBlob fetches h from the remote and stores it locally, verifying the
// digest via the re-hash on write.
func (s *Store) pullBlob(h Hash) error {
	rc, _, err := s.remote.PullBlobByDigest(h)
	if err != nil {
		return err
	}
	defer rc.Close()
	got, err := s.Blobs.Store(rc)
	if err != nil {
		return err
	}
	if !got.Equal(h) {
		// A mismatched digest means a corrupt/wrong remote blob. Remove the
		// bad blob we just wrote under the wrong hash is unnecessary (it is
		// content-addressed under `got`, not `h`); just report the failure.
		return fmt.Errorf("pull blob %s: remote returned digest %s", h, got)
	}
	return nil
}

// MapGet resolves an identity to its manifest blob hash, falling through to the
// remote output manifest on a local miss: it pulls every leaf, reconstructs the
// local "<hash> <name>" manifest byte-identically, stores it, and sets the map
// entry — turning the miss into a permanent local hit (§11.6). With no remote
// this is exactly Map.Get.
func (s *Store) MapGet(identity Hash) (Hash, error) {
	if h, err := s.Map.Get(identity); err == nil {
		return h, nil
	} else if s.remote == nil {
		return Hash{}, err
	}

	leaves, ok, err := s.remote.PullOutputManifest(identity)
	if err != nil {
		return Hash{}, err
	}
	if !ok {
		// Genuine miss — surface the local not-found semantics.
		return s.Map.Get(identity)
	}

	// Pull every leaf blob into the local store (re-hash verifies each).
	for _, leaf := range leaves {
		if err := s.EnsureBlob(leaf.Hash); err != nil {
			return Hash{}, fmt.Errorf("pull leaf %s: %w", leaf.Hash, err)
		}
	}

	// Reconstruct and store the local manifest, then set the map entry.
	manifestHash, err := s.Blobs.Store(strings.NewReader(serializeManifest(leaves)))
	if err != nil {
		return Hash{}, fmt.Errorf("store reconstructed manifest: %w", err)
	}
	if err := s.Map.Set(identity, manifestHash, true); err != nil {
		return Hash{}, fmt.Errorf("set map after pull-through: %w", err)
	}
	return manifestHash, nil
}
