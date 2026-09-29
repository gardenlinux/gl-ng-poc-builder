package artifact

import (
	"bufio"
	"fmt"
	"strings"

	"gl-ng/internal/objstore"
)

// SerializeManifest encodes outputs in the generic manifest format:
// "<hash> <name>\n" per entry. It delegates to objstore.SerializeManifest, the
// single source of truth for the byte format, so pull-through reconstruction
// (objstore.Store.MapGet) matches the engine's output exactly.
func SerializeManifest(outputs []Output) string {
	oo := make([]objstore.Output, len(outputs))
	for i, o := range outputs {
		oo[i] = objstore.Output{Name: o.Name, Hash: o.Hash}
	}
	return objstore.SerializeManifest(oo)
}

// storeManifest serializes outputs as "<hash> <name>\n" lines, stores as a blob,
// and maps identity → manifest blob hash.
func (e *Engine) storeManifest(identity objstore.Hash, outputs []Output) error {
	manifest := SerializeManifest(outputs)
	manifestHash, err := e.store.Blobs.Store(strings.NewReader(manifest))
	if err != nil {
		return fmt.Errorf("store manifest blob: %w", err)
	}
	return e.store.Map.Set(identity, manifestHash, false)
}

// parseManifestBlob reads and parses a manifest blob into its outputs.
func parseManifestBlob(store *objstore.Store, manifestHash objstore.Hash) ([]Output, error) {
	reader, err := store.Blobs.Open(manifestHash)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	var outputs []Output
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("malformed manifest line: %q", line)
		}
		hash, err := objstore.NewHash(parts[0])
		if err != nil {
			return nil, fmt.Errorf("invalid hash in manifest: %w", err)
		}
		outputs = append(outputs, Output{Name: parts[1], Hash: hash})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return outputs, nil
}

// ResolveOutputRefs is the shared implementation of Artifact.OutputRefs. Given
// an artifact's identity, it resolves identity → map → manifest blob and parses
// the leaf references, returning the manifest blob hash and the leaf hashes.
// Returns an error if the artifact has no map entry (not built).
func ResolveOutputRefs(a Artifact, store *objstore.Store) (objstore.Hash, []objstore.Hash, error) {
	identity, err := a.Identity()
	if err != nil {
		return objstore.Hash{}, nil, fmt.Errorf("compute identity: %w", err)
	}
	manifestHash, err := store.Map.Get(identity)
	if err != nil {
		return objstore.Hash{}, nil, fmt.Errorf("%s not built: %w", a, err)
	}
	outputs, err := parseManifestBlob(store, manifestHash)
	if err != nil {
		return objstore.Hash{}, nil, fmt.Errorf("parse manifest for %s: %w", a, err)
	}
	leaves := make([]objstore.Hash, 0, len(outputs))
	for _, o := range outputs {
		leaves = append(leaves, o.Hash)
	}
	return manifestHash, leaves, nil
}

// loadManifest reads the manifest for the given identity and parses it. It
// uses Store.MapGet, so on a local miss it falls through to the remote:
// reconstructing the manifest and pulling every leaf turns a would-be rebuild
// into a pull-through cache hit (oci-cache-design.md §11.6). With no remote
// configured this is exactly the old local Map.Get + parse.
func (e *Engine) loadManifest(identity objstore.Hash) ([]Output, error) {
	manifestHash, err := e.store.MapGet(identity)
	if err != nil {
		return nil, err
	}
	return parseManifestBlob(e.store, manifestHash)
}
