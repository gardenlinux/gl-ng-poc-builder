package ociclient

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"gl-ng/internal/objstore"
)

// testClient returns a Client for the registry named by GL_TEST_REGISTRY
// (e.g. "localhost:5000/gl-ng"), skipping if unset — mirroring how
// stub-dependent tests gate on GL_EXEC_ENV_STUB. Each test uses a unique
// repository name so runs do not collide.
func testClient(t *testing.T) *Client {
	t.Helper()
	ref := os.Getenv("GL_TEST_REGISTRY")
	if ref == "" {
		t.Skip("GL_TEST_REGISTRY unset; skipping registry-dependent test")
	}
	registry, name := SplitRef(ref)
	if name == "" {
		name = "gl-ng"
	}
	// Namespace per-test to avoid cross-test tag/blob interference.
	name = fmt.Sprintf("%s-test-%s", name, strings.ToLower(t.Name()))
	c := New(registry, name)
	if !c.Reachable() {
		t.Skipf("registry %s not reachable", registry)
	}
	return c
}

func hashOf(b []byte) objstore.Hash {
	sum := sha256.Sum256(b)
	return objstore.MustHash(hex.EncodeToString(sum[:]))
}

func TestClient_BlobRoundTrip(t *testing.T) {
	c := testClient(t)
	// Unique per run so a persistent registry from a prior run does not
	// already hold this exact blob (the point below is that a *fresh* blob
	// is absent, then present after push).
	content := []byte(fmt.Sprintf("hello ociclient %s %d", t.Name(), time.Now().UnixNano()))
	h := hashOf(content)

	has, err := c.HasBlob(h)
	if err != nil {
		t.Fatalf("HasBlob: %v", err)
	}
	if has {
		t.Fatalf("blob unexpectedly already present")
	}

	if err := c.PushBlob(h, bytes.NewReader(content), int64(len(content))); err != nil {
		t.Fatalf("PushBlob: %v", err)
	}

	has, err = c.HasBlob(h)
	if err != nil || !has {
		t.Fatalf("HasBlob after push: has=%v err=%v", has, err)
	}

	rc, size, err := c.PullBlob(h)
	if err != nil {
		t.Fatalf("PullBlob: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, content) {
		t.Errorf("pulled content mismatch: got %q", got)
	}
	if size >= 0 && size != int64(len(content)) {
		t.Errorf("pulled size = %d, want %d", size, len(content))
	}
	// Digest verification: the pulled bytes must hash back to h.
	if hashOf(got) != h {
		t.Errorf("pulled blob digest mismatch")
	}

	// Re-push is a no-op (idempotent).
	if err := c.PushBlob(h, bytes.NewReader(content), int64(len(content))); err != nil {
		t.Fatalf("re-PushBlob: %v", err)
	}
}

func TestClient_MissingBlobAndManifest(t *testing.T) {
	c := testClient(t)
	absent := objstore.MustHash("dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")

	if _, _, err := c.PullBlob(absent); err != ErrNotFound {
		t.Errorf("PullBlob(absent) err = %v, want ErrNotFound", err)
	}
	_, _, ok, err := c.GetManifest("build-artifact-absent")
	if err != nil {
		t.Fatalf("GetManifest(absent): unexpected error %v", err)
	}
	if ok {
		t.Errorf("GetManifest(absent) ok = true, want false")
	}
}

func TestClient_ManifestPutGetParse(t *testing.T) {
	c := testClient(t)

	// One leaf blob.
	leaf := []byte("leaf-content")
	lh := hashOf(leaf)
	if err := c.PushBlob(lh, bytes.NewReader(leaf), int64(len(leaf))); err != nil {
		t.Fatalf("PushBlob leaf: %v", err)
	}

	identity := objstore.MustHash("1111111111111111111111111111111111111111111111111111111111111111")
	body, err := BuildOutputManifest(identity, []LeafRef{{Hash: lh, Name: "rootfs.tar.gz", Size: int64(len(leaf))}})
	if err != nil {
		t.Fatalf("BuildOutputManifest: %v", err)
	}
	tag := TagPrefixOutput + identity.String()
	if err := c.PutManifest(tag, body, MediaTypeManifest); err != nil {
		t.Fatalf("PutManifest: %v", err)
	}

	got, mt, ok, err := c.GetManifest(tag)
	if err != nil || !ok {
		t.Fatalf("GetManifest: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(mt, "manifest") {
		t.Errorf("media type = %q", mt)
	}
	m, err := ParseManifest(got)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if m.ArtifactType != ArtifactTypeOutput {
		t.Errorf("artifactType = %q, want %q", m.ArtifactType, ArtifactTypeOutput)
	}
	if len(m.Layers) != 1 {
		t.Fatalf("layers = %d, want 1", len(m.Layers))
	}
	if m.Layers[0].Digest != "sha256:"+lh.String() {
		t.Errorf("layer digest = %q", m.Layers[0].Digest)
	}
	if m.Layers[0].Annotations[AnnotationTitle] != "rootfs.tar.gz" {
		t.Errorf("layer title = %q", m.Layers[0].Annotations[AnnotationTitle])
	}
	if m.Annotations[AnnotationIdentity] != identity.String() {
		t.Errorf("identity annotation = %q", m.Annotations[AnnotationIdentity])
	}
	if m.Config.MediaType != MediaTypeEmptyJSON {
		t.Errorf("config media type = %q", m.Config.MediaType)
	}
}

func TestClient_ListTagsPrefixFilter(t *testing.T) {
	c := testClient(t)

	leaf := []byte("x")
	lh := hashOf(leaf)
	c.PushBlob(lh, bytes.NewReader(leaf), 1)

	id1 := objstore.MustHash("1111111111111111111111111111111111111111111111111111111111111111")
	id2 := objstore.MustHash("2222222222222222222222222222222222222222222222222222222222222222")
	for _, id := range []objstore.Hash{id1, id2} {
		body, _ := BuildOutputManifest(id, []LeafRef{{Hash: lh, Name: "n", Size: 1}})
		if err := c.PutManifest(TagPrefixOutput+id.String(), body, MediaTypeManifest); err != nil {
			t.Fatalf("PutManifest: %v", err)
		}
	}
	pinBody, _ := BuildPinManifest(objstore.PinKindImport, "deadbeefdeadbeef", "orig", []objstore.Hash{lh}, map[objstore.Hash]int64{lh: 1})
	if err := c.PutManifest(TagPrefixImport+"deadbeefdeadbeef", pinBody, MediaTypeManifest); err != nil {
		t.Fatalf("PutManifest pin: %v", err)
	}

	tags, err := c.ListTags()
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	outputs := 0
	imports := 0
	for _, tag := range tags {
		if strings.HasPrefix(tag, TagPrefixOutput) {
			outputs++
		}
		if strings.HasPrefix(tag, TagPrefixImport) {
			imports++
		}
	}
	if outputs != 2 {
		t.Errorf("output tags = %d, want 2 (%v)", outputs, tags)
	}
	if imports != 1 {
		t.Errorf("import tags = %d, want 1 (%v)", imports, tags)
	}
}
