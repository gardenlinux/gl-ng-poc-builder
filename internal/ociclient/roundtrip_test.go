package ociclient

import (
	"bytes"
	"fmt"
	"io"
	"testing"
	"time"

	"gl-ng/internal/objstore"
)

// TestRoundTrip_PushThenPullThrough is the §12 acceptance gate: publish an
// output (leaves + manifest) from a producer to the registry, then point a
// fresh empty store's remote at that registry and confirm a MapGet fully
// reconstructs the manifest locally (byte-identical to the canonical
// serialization) and materializes every leaf blob. Gated on GL_TEST_REGISTRY.
func TestRoundTrip_PushThenPullThrough(t *testing.T) {
	c := testClient(t)

	// Producer side: two leaves, unique per run so a persistent registry does
	// not shortcut the pull.
	nonce := time.Now().UnixNano()
	leaf1 := []byte(fmt.Sprintf("rootfs-tarball %d", nonce))
	leaf2 := []byte(fmt.Sprintf("control-stanza %d", nonce))
	h1 := hashOf(leaf1)
	h2 := hashOf(leaf2)
	if err := c.PushBlob(h1, bytes.NewReader(leaf1), int64(len(leaf1))); err != nil {
		t.Fatalf("push leaf1: %v", err)
	}
	if err := c.PushBlob(h2, bytes.NewReader(leaf2), int64(len(leaf2))); err != nil {
		t.Fatalf("push leaf2: %v", err)
	}

	// Identity derived from the nonce so tags don't collide across runs.
	identity := hashOf([]byte(fmt.Sprintf("identity %d", nonce)))
	leaves := []objstore.Output{
		{Name: "rootfs.tar.gz", Hash: h1},
		{Name: "control:libc6", Hash: h2},
	}
	body, err := BuildOutputManifest(identity, []LeafRef{
		{Hash: h1, Name: "rootfs.tar.gz", Size: int64(len(leaf1))},
		{Hash: h2, Name: "control:libc6", Size: int64(len(leaf2))},
	})
	if err != nil {
		t.Fatalf("build manifest: %v", err)
	}
	if err := c.PutManifest(TagPrefixOutput+identity.String(), body, MediaTypeManifest); err != nil {
		t.Fatalf("put manifest: %v", err)
	}

	// Consumer side: a fresh, empty store whose remote points at the registry.
	store, err := objstore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	store.SetRemote(NewRemote(c))

	if store.Map.Has(identity) {
		t.Fatal("consumer store should start empty")
	}

	manifestHash, err := store.MapGet(identity)
	if err != nil {
		t.Fatalf("MapGet (pull-through): %v", err)
	}

	// Reconstructed manifest is byte-identical to the canonical serialization.
	rc, err := store.Blobs.Open(manifestHash)
	if err != nil {
		t.Fatalf("open reconstructed manifest: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != objstore.SerializeManifest(leaves) {
		t.Errorf("manifest not byte-identical:\n got: %q\nwant: %q", got, objstore.SerializeManifest(leaves))
	}

	// Both leaves materialized locally, content-verified by re-hash on store.
	for _, l := range leaves {
		if !store.Blobs.Has(l.Hash) {
			t.Errorf("leaf %s not pulled", l.Name)
		}
	}

	// A leaf pulled purely by digest via OpenBlob (independent of the map).
	store2, err := objstore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store2: %v", err)
	}
	store2.SetRemote(NewRemote(c))
	brc, err := store2.OpenBlob(h1)
	if err != nil {
		t.Fatalf("OpenBlob pull-through: %v", err)
	}
	pulled, _ := io.ReadAll(brc)
	brc.Close()
	if !bytes.Equal(pulled, leaf1) {
		t.Errorf("OpenBlob content mismatch")
	}
}
