package objstore

import (
	"os"
	"path/filepath"
	"testing"
)

func setupPins(t *testing.T) *Pins {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "pins")
	p, err := newPins(dir)
	if err != nil {
		t.Fatalf("newPins: %v", err)
	}
	return p
}

func TestPins_CreateListGetRoundTrip(t *testing.T) {
	p := setupPins(t)

	h1 := MustHash("1111111111111111111111111111111111111111111111111111111111111111")
	h2 := MustHash("2222222222222222222222222222222222222222222222222222222222222222")

	id, err := p.Create("libfoo 1.2.3 orig", []Hash{h1, h2})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !pinIDPattern.MatchString(id) {
		t.Fatalf("id %q is not 16 lowercase hex chars", id)
	}

	pins := p.List()
	if len(pins) != 1 {
		t.Fatalf("List: got %d pins, want 1", len(pins))
	}
	if pins[0].ID != id || pins[0].Name != "libfoo 1.2.3 orig" {
		t.Errorf("List: got %+v", pins[0])
	}
	if len(pins[0].Blobs) != 2 {
		t.Errorf("List: got %d blobs, want 2", len(pins[0].Blobs))
	}

	got, err := p.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "libfoo 1.2.3 orig" || len(got.Blobs) != 2 {
		t.Errorf("Get: got %+v", got)
	}
	if !got.Blobs[0].Equal(h1) || !got.Blobs[1].Equal(h2) {
		t.Errorf("Get: blob hashes not round-tripped: %+v", got.Blobs)
	}
}

func TestPins_IDsAreUnique(t *testing.T) {
	p := setupPins(t)
	h := MustHash("1111111111111111111111111111111111111111111111111111111111111111")

	seen := make(map[string]bool)
	for i := 0; i < 200; i++ {
		id, err := p.Create("x", []Hash{h})
		if err != nil {
			t.Fatalf("Create #%d: %v", i, err)
		}
		if !pinIDPattern.MatchString(id) {
			t.Fatalf("id %q not 16-hex", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestPins_ReachableBlobsDedupsAcrossPins(t *testing.T) {
	p := setupPins(t)
	shared := MustHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	only1 := MustHash("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	only2 := MustHash("cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")

	if _, err := p.Create("pin1", []Hash{shared, only1}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Create("pin2", []Hash{shared, only2}); err != nil {
		t.Fatal(err)
	}

	reach := p.ReachableBlobs()
	if len(reach) != 3 {
		t.Fatalf("ReachableBlobs: got %d, want 3 (shared counted once)", len(reach))
	}
	for _, h := range []Hash{shared, only1, only2} {
		if _, ok := reach[h]; !ok {
			t.Errorf("ReachableBlobs missing %s", h)
		}
	}
}

func TestPins_DropRemovesOnlyNamedPinAndKeepsSharedBlobs(t *testing.T) {
	p := setupPins(t)
	shared := MustHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	only1 := MustHash("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	id1, _ := p.Create("pin1", []Hash{shared, only1})
	_, _ = p.Create("pin2", []Hash{shared})

	if err := p.Drop(id1); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if _, err := p.Get(id1); err == nil {
		t.Error("Get should fail for dropped pin")
	}

	reach := p.ReachableBlobs()
	// shared survives (pin2 still lists it); only1 is gone.
	if _, ok := reach[shared]; !ok {
		t.Error("shared blob should survive drop (still listed by pin2)")
	}
	if _, ok := reach[only1]; ok {
		t.Error("only1 should be gone after dropping pin1")
	}

	if err := p.Drop(id1); err == nil {
		t.Error("dropping a non-existent pin should error")
	}
}

func TestPins_MalformedFileIsSkipped(t *testing.T) {
	p := setupPins(t)
	good := MustHash("1111111111111111111111111111111111111111111111111111111111111111")
	goodID, _ := p.Create("good", []Hash{good})

	// A file with a valid-looking id but garbage YAML must be skipped, not fatal.
	badID := "deadbeefdeadbeef"
	if err := os.WriteFile(p.path(badID), []byte("this: is: not: valid: yaml: [[["), 0o644); err != nil {
		t.Fatal(err)
	}
	// A file that doesn't match the id pattern must be ignored entirely.
	if err := os.WriteFile(filepath.Join(p.root, "notapin.yml"), []byte("name: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pins := p.List()
	if len(pins) != 1 || pins[0].ID != goodID {
		t.Fatalf("List should return only the good pin, got %+v", pins)
	}
	// ReachableBlobs should also tolerate the malformed file.
	if _, ok := p.ReachableBlobs()[good]; !ok {
		t.Error("ReachableBlobs should still contain the good blob")
	}
}

func TestPins_GetRejectsInvalidID(t *testing.T) {
	p := setupPins(t)
	if _, err := p.Get("not-a-valid-id"); err == nil {
		t.Error("Get should reject an invalid id")
	}
	if _, err := p.Get("deadbeefdeadbeef"); err == nil {
		t.Error("Get should error for a well-formed but absent id")
	}
}

// TestPins_BuildDepsAutoPinShape pins the invariant that gl lockfile's auto-pin
// relies on (oci-cache-design.md §4): one pin per arch covers the lockfile index
// blob plus every resolved .deb; a second arch produces a distinct pin; and a
// .deb shared by both arches survives dropping either single pin.
func TestPins_BuildDepsAutoPinShape(t *testing.T) {
	p := setupPins(t)

	index1 := MustHash("1111111111111111111111111111111111111111111111111111111111111111")
	index2 := MustHash("2222222222222222222222222222222222222222222222222222222222222222")
	sharedDeb := MustHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	amd64Deb := MustHash("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	arm64Deb := MustHash("cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")

	// Mirror generate.go: Create(name, append([]Hash{index}, debHashes...)).
	amd64ID, err := p.Create("pkg build-deps (amd64, ...)",
		append([]Hash{index1}, sharedDeb, amd64Deb))
	if err != nil {
		t.Fatalf("Create amd64: %v", err)
	}
	arm64ID, err := p.Create("pkg build-deps (arm64, ...)",
		append([]Hash{index2}, sharedDeb, arm64Deb))
	if err != nil {
		t.Fatalf("Create arm64: %v", err)
	}
	if amd64ID == arm64ID {
		t.Fatal("each arch must get a distinct pin")
	}

	// One pin per arch fully covers that arch's index + debs.
	amd64Pin, _ := p.Get(amd64ID)
	got := make(map[Hash]bool)
	for _, h := range amd64Pin.Blobs {
		got[h] = true
	}
	for _, h := range []Hash{index1, sharedDeb, amd64Deb} {
		if !got[h] {
			t.Errorf("amd64 pin missing %s", h)
		}
	}

	// Dropping the arm64 pin must keep the shared .deb (still named by amd64) but
	// release arm64's exclusive index and .deb.
	if err := p.Drop(arm64ID); err != nil {
		t.Fatalf("Drop arm64: %v", err)
	}
	reach := p.ReachableBlobs()
	if _, ok := reach[sharedDeb]; !ok {
		t.Error("shared .deb must survive dropping one arch's pin")
	}
	if _, ok := reach[index1]; !ok {
		t.Error("surviving amd64 pin must still protect its index blob")
	}
	if _, ok := reach[amd64Deb]; !ok {
		t.Error("surviving amd64 pin must still protect its .deb")
	}
	if _, ok := reach[index2]; ok {
		t.Error("arm64-exclusive index must be released after dropping the arm64 pin")
	}
	if _, ok := reach[arm64Deb]; ok {
		t.Error("arm64-exclusive .deb must be released after dropping the arm64 pin")
	}
}
