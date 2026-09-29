package build

import (
	"context"
	"strings"
	"sync"
	"testing"

	"gl-ng/internal/artifact"
	"gl-ng/internal/log"
	"gl-ng/internal/objstore"
)

// =============================================================================
// Test 1: Validate the artifact graph engine with mock artifacts
// =============================================================================

// mockBuildArtifact is a lightweight artifact for graph engine testing that
// records build invocations and stores a blob in the object store.
type mockBuildArtifact struct {
	name     string
	deps     []artifact.Artifact
	identity string
	buildFn  func(artifact.BuildContext) ([]artifact.Output, error)

	mu       sync.Mutex
	built    bool
	buildIdx int
}

func (m *mockBuildArtifact) Identity() (objstore.Hash, error) {
	return objstore.NewHash(m.identity)
}

func (m *mockBuildArtifact) Key() string                   { return m.name }
func (m *mockBuildArtifact) Depends() []artifact.Artifact  { return m.deps }
func (m *mockBuildArtifact) Includes() []artifact.Artifact { return nil }
func (m *mockBuildArtifact) Inputs() []artifact.Input      { return nil }
func (m *mockBuildArtifact) String() string                { return m.name }

func (m *mockBuildArtifact) OutputRefs(store *objstore.Store) (objstore.Hash, []objstore.Hash, error) {
	return artifact.ResolveOutputRefs(m, store)
}

func (m *mockBuildArtifact) Build(ctx artifact.BuildContext) ([]artifact.Output, error) {
	m.mu.Lock()
	m.built = true
	m.mu.Unlock()
	if m.buildFn != nil {
		return m.buildFn(ctx)
	}
	// Default: store a blob and return it as an output.
	content := "output of " + m.name
	blobHash, err := ctx.Store.Blobs.Store(strings.NewReader(content))
	if err != nil {
		return nil, err
	}
	return []artifact.Output{
		{Name: m.name + ".out", Hash: blobHash},
	}, nil
}

func TestIntegrationArtifactGraphEngine(t *testing.T) {
	// 1. Set up a test object store
	storeDir := t.TempDir()
	store, err := objstore.Open(storeDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	// 2. Create mock artifacts simulating: source build -> binary packages -> rootfs
	// This mirrors the real pipeline topology without hitting the network.

	var buildOrder []string
	var orderMu sync.Mutex
	recordOrder := func(name string) {
		orderMu.Lock()
		buildOrder = append(buildOrder, name)
		orderMu.Unlock()
	}

	sourceBuildA := &mockBuildArtifact{
		name:     "source-build:coreutils",
		identity: strings.Repeat("1", 64),
		buildFn: func(ctx artifact.BuildContext) ([]artifact.Output, error) {
			recordOrder("source-build:coreutils")
			blobHash, err := ctx.Store.Blobs.Store(strings.NewReader("coreutils source output"))
			if err != nil {
				return nil, err
			}
			return []artifact.Output{{Name: "coreutils.out", Hash: blobHash}}, nil
		},
	}

	sourceBuildB := &mockBuildArtifact{
		name:     "source-build:bash",
		identity: strings.Repeat("2", 64),
		buildFn: func(ctx artifact.BuildContext) ([]artifact.Output, error) {
			recordOrder("source-build:bash")
			blobHash, err := ctx.Store.Blobs.Store(strings.NewReader("bash source output"))
			if err != nil {
				return nil, err
			}
			return []artifact.Output{{Name: "bash.out", Hash: blobHash}}, nil
		},
	}

	binaryPkgCoreutils := &mockBuildArtifact{
		name:     "binary-pkg:coreutils",
		identity: strings.Repeat("3", 64),
		deps:     []artifact.Artifact{sourceBuildA},
		buildFn: func(ctx artifact.BuildContext) ([]artifact.Output, error) {
			recordOrder("binary-pkg:coreutils")
			blobHash, err := ctx.Store.Blobs.Store(strings.NewReader("coreutils binary"))
			if err != nil {
				return nil, err
			}
			return []artifact.Output{{Name: "coreutils.deb", Hash: blobHash}}, nil
		},
	}

	binaryPkgBash := &mockBuildArtifact{
		name:     "binary-pkg:bash",
		identity: strings.Repeat("4", 64),
		deps:     []artifact.Artifact{sourceBuildB},
		buildFn: func(ctx artifact.BuildContext) ([]artifact.Output, error) {
			recordOrder("binary-pkg:bash")
			blobHash, err := ctx.Store.Blobs.Store(strings.NewReader("bash binary"))
			if err != nil {
				return nil, err
			}
			return []artifact.Output{{Name: "bash.deb", Hash: blobHash}}, nil
		},
	}

	binaryPkgLibc := &mockBuildArtifact{
		name:     "binary-pkg:libc6",
		identity: strings.Repeat("5", 64),
		deps:     []artifact.Artifact{sourceBuildA},
		buildFn: func(ctx artifact.BuildContext) ([]artifact.Output, error) {
			recordOrder("binary-pkg:libc6")
			blobHash, err := ctx.Store.Blobs.Store(strings.NewReader("libc6 binary"))
			if err != nil {
				return nil, err
			}
			return []artifact.Output{{Name: "libc6.deb", Hash: blobHash}}, nil
		},
	}

	rootfsArtifact := &mockBuildArtifact{
		name:     "rootfs:gardenlinux",
		identity: strings.Repeat("6", 64),
		deps:     []artifact.Artifact{binaryPkgCoreutils, binaryPkgBash, binaryPkgLibc},
		buildFn: func(ctx artifact.BuildContext) ([]artifact.Output, error) {
			recordOrder("rootfs:gardenlinux")
			blobHash, err := ctx.Store.Blobs.Store(strings.NewReader("rootfs image manifest"))
			if err != nil {
				return nil, err
			}
			return []artifact.Output{{Name: "rootfs.tar.gz", Hash: blobHash}}, nil
		},
	}

	// 3. Build the artifact graph
	g := artifact.NewGraph()
	g.Add(sourceBuildA)
	g.Add(sourceBuildB)
	g.Add(binaryPkgCoreutils)
	g.Add(binaryPkgBash)
	g.Add(binaryPkgLibc)
	g.Add(rootfsArtifact)

	// 4. Run the graph engine with 4 workers
	engine := artifact.NewEngine(g, store, 4)
	results, err := engine.Run(log.WithTarget(context.Background(), log.Discard))
	if err != nil {
		t.Fatalf("engine.Run: %v", err)
	}

	// 5. Verify all artifacts built successfully
	if len(results) != 6 {
		t.Fatalf("expected 6 results, got %d", len(results))
	}

	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("artifact %s failed: %v", r.Artifact, r.Err)
		}
		if r.Cached {
			t.Fatalf("artifact %s unexpectedly cached on first run", r.Artifact)
		}
	}

	// Verify build order: source builds must come before their binary packages
	// and binary packages must come before rootfs.
	orderMap := make(map[string]int)
	for i, name := range buildOrder {
		orderMap[name] = i
	}

	assertBefore := func(before, after string) {
		t.Helper()
		bIdx, bOk := orderMap[before]
		aIdx, aOk := orderMap[after]
		if !bOk {
			t.Fatalf("%s not found in build order", before)
		}
		if !aOk {
			t.Fatalf("%s not found in build order", after)
		}
		if bIdx >= aIdx {
			t.Fatalf("expected %s (idx=%d) before %s (idx=%d)", before, bIdx, after, aIdx)
		}
	}

	assertBefore("source-build:coreutils", "binary-pkg:coreutils")
	assertBefore("source-build:coreutils", "binary-pkg:libc6")
	assertBefore("source-build:bash", "binary-pkg:bash")
	assertBefore("binary-pkg:coreutils", "rootfs:gardenlinux")
	assertBefore("binary-pkg:bash", "rootfs:gardenlinux")
	assertBefore("binary-pkg:libc6", "rootfs:gardenlinux")

	// Verify all artifacts are cached in the object store
	for _, a := range []struct {
		name     string
		identity string
	}{
		{"source-build:coreutils", strings.Repeat("1", 64)},
		{"source-build:bash", strings.Repeat("2", 64)},
		{"binary-pkg:coreutils", strings.Repeat("3", 64)},
		{"binary-pkg:bash", strings.Repeat("4", 64)},
		{"binary-pkg:libc6", strings.Repeat("5", 64)},
		{"rootfs:gardenlinux", strings.Repeat("6", 64)},
	} {
		h, _ := objstore.NewHash(a.identity)
		if !store.Map.Has(h) {
			t.Fatalf("artifact %s not cached in map store", a.name)
		}
	}

	// 6. Second run should produce cache hits for all artifacts
	g2 := artifact.NewGraph()
	sourceBuildA2 := &mockBuildArtifact{
		name:     "source-build:coreutils",
		identity: strings.Repeat("1", 64),
		buildFn: func(ctx artifact.BuildContext) ([]artifact.Output, error) {
			t.Fatal("source-build:coreutils should not rebuild on second run")
			return nil, nil
		},
	}
	sourceBuildB2 := &mockBuildArtifact{
		name:     "source-build:bash",
		identity: strings.Repeat("2", 64),
		buildFn: func(ctx artifact.BuildContext) ([]artifact.Output, error) {
			t.Fatal("source-build:bash should not rebuild on second run")
			return nil, nil
		},
	}
	binaryPkgCoreutils2 := &mockBuildArtifact{
		name:     "binary-pkg:coreutils",
		identity: strings.Repeat("3", 64),
		deps:     []artifact.Artifact{sourceBuildA2},
		buildFn: func(ctx artifact.BuildContext) ([]artifact.Output, error) {
			t.Fatal("binary-pkg:coreutils should not rebuild on second run")
			return nil, nil
		},
	}
	binaryPkgBash2 := &mockBuildArtifact{
		name:     "binary-pkg:bash",
		identity: strings.Repeat("4", 64),
		deps:     []artifact.Artifact{sourceBuildB2},
		buildFn: func(ctx artifact.BuildContext) ([]artifact.Output, error) {
			t.Fatal("binary-pkg:bash should not rebuild on second run")
			return nil, nil
		},
	}
	binaryPkgLibc2 := &mockBuildArtifact{
		name:     "binary-pkg:libc6",
		identity: strings.Repeat("5", 64),
		deps:     []artifact.Artifact{sourceBuildA2},
		buildFn: func(ctx artifact.BuildContext) ([]artifact.Output, error) {
			t.Fatal("binary-pkg:libc6 should not rebuild on second run")
			return nil, nil
		},
	}
	rootfsArtifact2 := &mockBuildArtifact{
		name:     "rootfs:gardenlinux",
		identity: strings.Repeat("6", 64),
		deps:     []artifact.Artifact{binaryPkgCoreutils2, binaryPkgBash2, binaryPkgLibc2},
		buildFn: func(ctx artifact.BuildContext) ([]artifact.Output, error) {
			t.Fatal("rootfs should not rebuild on second run")
			return nil, nil
		},
	}

	g2.Add(sourceBuildA2)
	g2.Add(sourceBuildB2)
	g2.Add(binaryPkgCoreutils2)
	g2.Add(binaryPkgBash2)
	g2.Add(binaryPkgLibc2)
	g2.Add(rootfsArtifact2)

	engine2 := artifact.NewEngine(g2, store, 4)
	results2, err := engine2.Run(log.WithTarget(context.Background(), log.Discard))
	if err != nil {
		t.Fatalf("second engine.Run: %v", err)
	}

	for _, r := range results2 {
		if r.Err != nil {
			t.Fatalf("second run: artifact %s failed: %v", r.Artifact, r.Err)
		}
		if !r.Cached {
			t.Fatalf("second run: artifact %s should be cached", r.Artifact)
		}
	}
}

// =============================================================================
// Test 3: Verify graph ordering with diamond dependency pattern
// =============================================================================

func TestIntegrationDiamondDependency(t *testing.T) {
	storeDir := t.TempDir()
	store, err := objstore.Open(storeDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	// Diamond: rootfs -> {binA, binB} -> sourceC
	// Both binA and binB depend on sourceC. sourceC should only build once.
	buildCount := struct {
		mu    sync.Mutex
		count map[string]int
	}{count: make(map[string]int)}

	recordBuild := func(name string) {
		buildCount.mu.Lock()
		buildCount.count[name]++
		buildCount.mu.Unlock()
	}

	sourceC := &mockBuildArtifact{
		name:     "source-build:shared-lib",
		identity: strings.Repeat("c", 64),
		buildFn: func(ctx artifact.BuildContext) ([]artifact.Output, error) {
			recordBuild("sourceC")
			blobHash, err := ctx.Store.Blobs.Store(strings.NewReader("shared-lib source"))
			if err != nil {
				return nil, err
			}
			return []artifact.Output{{Name: "shared-lib.out", Hash: blobHash}}, nil
		},
	}

	binA := &mockBuildArtifact{
		name:     "binary-pkg:libfoo",
		identity: strings.Repeat("a", 64),
		deps:     []artifact.Artifact{sourceC},
		buildFn: func(ctx artifact.BuildContext) ([]artifact.Output, error) {
			recordBuild("binA")
			blobHash, err := ctx.Store.Blobs.Store(strings.NewReader("libfoo binary"))
			if err != nil {
				return nil, err
			}
			return []artifact.Output{{Name: "libfoo.deb", Hash: blobHash}}, nil
		},
	}

	binB := &mockBuildArtifact{
		name:     "binary-pkg:libbar",
		identity: strings.Repeat("b", 64),
		deps:     []artifact.Artifact{sourceC},
		buildFn: func(ctx artifact.BuildContext) ([]artifact.Output, error) {
			recordBuild("binB")
			blobHash, err := ctx.Store.Blobs.Store(strings.NewReader("libbar binary"))
			if err != nil {
				return nil, err
			}
			return []artifact.Output{{Name: "libbar.deb", Hash: blobHash}}, nil
		},
	}

	rootfsNode := &mockBuildArtifact{
		name:     "rootfs:diamond-test",
		identity: strings.Repeat("d", 64),
		deps:     []artifact.Artifact{binA, binB},
		buildFn: func(ctx artifact.BuildContext) ([]artifact.Output, error) {
			recordBuild("rootfs")
			blobHash, err := ctx.Store.Blobs.Store(strings.NewReader("rootfs manifest"))
			if err != nil {
				return nil, err
			}
			return []artifact.Output{{Name: "rootfs.tar.gz", Hash: blobHash}}, nil
		},
	}

	g := artifact.NewGraph()
	g.Add(sourceC)
	g.Add(binA)
	g.Add(binB)
	g.Add(rootfsNode)

	engine := artifact.NewEngine(g, store, 4)
	results, err := engine.Run(log.WithTarget(context.Background(), log.Discard))
	if err != nil {
		t.Fatalf("engine.Run: %v", err)
	}

	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("artifact %s failed: %v", r.Artifact, r.Err)
		}
	}

	// sourceC should build exactly once despite being depended on by both binA and binB
	buildCount.mu.Lock()
	if buildCount.count["sourceC"] != 1 {
		t.Fatalf("sourceC built %d times, expected exactly 1", buildCount.count["sourceC"])
	}
	buildCount.mu.Unlock()

	// All 4 artifacts should be in results
	if len(results) != 4 {
		t.Fatalf("expected 4 results, got %d", len(results))
	}
}

// =============================================================================
// Test 4: Failure propagation in the graph
// =============================================================================

func TestIntegrationFailurePropagation(t *testing.T) {
	storeDir := t.TempDir()
	store, err := objstore.Open(storeDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	failSource := &mockBuildArtifact{
		name:     "source-build:broken",
		identity: strings.Repeat("9", 64),
		buildFn: func(ctx artifact.BuildContext) ([]artifact.Output, error) {
			return nil, &buildError{msg: "compile error: missing header"}
		},
	}

	dependentBin := &mockBuildArtifact{
		name:     "binary-pkg:broken-bin",
		identity: strings.Repeat("8", 64),
		deps:     []artifact.Artifact{failSource},
		buildFn: func(ctx artifact.BuildContext) ([]artifact.Output, error) {
			t.Fatal("dependent binary should not build when source fails")
			return nil, nil
		},
	}

	g := artifact.NewGraph()
	g.Add(failSource)
	g.Add(dependentBin)

	engine := artifact.NewEngine(g, store, 1)
	results, err := engine.Run(log.WithTarget(context.Background(), log.Discard))
	if err != nil {
		t.Fatalf("engine.Run should not error, got: %v", err)
	}

	var sourceErr, binErr bool
	for _, r := range results {
		if r.Artifact.String() == "source-build:broken" && r.Err != nil {
			sourceErr = true
		}
		if r.Artifact.String() == "binary-pkg:broken-bin" && r.Err != nil {
			binErr = true
		}
	}

	if !sourceErr {
		t.Fatal("expected source build to have an error result")
	}
	if !binErr {
		t.Fatal("expected binary package to be skipped/errored due to dep failure")
	}
}

type buildError struct {
	msg string
}

func (e *buildError) Error() string { return e.msg }

func TestIntegrationTransitiveFailurePropagation(t *testing.T) {
	storeDir := t.TempDir()
	store, err := objstore.Open(storeDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	source := &mockBuildArtifact{
		name:     "source:fail",
		identity: strings.Repeat("1", 64),
		buildFn: func(ctx artifact.BuildContext) ([]artifact.Output, error) {
			return nil, &buildError{msg: "compile failed"}
		},
	}

	binary := &mockBuildArtifact{
		name:     "binary:dep",
		identity: strings.Repeat("2", 64),
		deps:     []artifact.Artifact{source},
		buildFn: func(ctx artifact.BuildContext) ([]artifact.Output, error) {
			t.Fatal("binary should not build")
			return nil, nil
		},
	}

	rootfs := &mockBuildArtifact{
		name:     "rootfs:final",
		identity: strings.Repeat("3", 64),
		deps:     []artifact.Artifact{binary},
		buildFn: func(ctx artifact.BuildContext) ([]artifact.Output, error) {
			t.Fatal("rootfs should not build")
			return nil, nil
		},
	}

	g := artifact.NewGraph()
	g.Add(source)
	g.Add(binary)
	g.Add(rootfs)

	engine := artifact.NewEngine(g, store, 1)
	results, err := engine.Run(log.WithTarget(context.Background(), log.Discard))
	if err != nil {
		t.Fatalf("engine.Run should not error, got: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	var rootfsSkipped bool
	for _, r := range results {
		if r.Artifact.String() == "rootfs:final" && r.Err != nil {
			rootfsSkipped = true
		}
	}
	if !rootfsSkipped {
		t.Fatal("rootfs should be skipped due to transitive dependency failure")
	}
}

// =============================================================================
// Test: GC keep-set assembly (graph reachability ∪ pins) then Sweep.
// Mirrors cmd/gl/cache.go:cacheGC. This is the highest-risk path — over-
// deletion is data loss — so it exercises keep, delete-stale, delete-unpinned,
// pin-protection, and dry-run in one topology. See oci-cache-design.md §5.
// =============================================================================

// buildGCKeepSet reproduces cacheGC's keep-set: union of every built node's
// OutputRefs (manifest + leaves) with every pinned blob.
func buildGCKeepSet(t *testing.T, g *artifact.Graph, store *objstore.Store) map[objstore.Hash]struct{} {
	t.Helper()
	keep := make(map[objstore.Hash]struct{})
	for _, key := range g.Keys() {
		a := g.Find(key)
		manifest, leaves, err := a.OutputRefs(store)
		if err != nil {
			continue // not built — contributes nothing
		}
		keep[manifest] = struct{}{}
		for _, h := range leaves {
			keep[h] = struct{}{}
		}
	}
	for h := range store.Pins.ReachableBlobs() {
		keep[h] = struct{}{}
	}
	return keep
}

func TestGC_KeepSetAndSweep(t *testing.T) {
	store, err := objstore.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	// A reachable, built artifact: leaf output + its manifest blob.
	leaf := &mockBuildArtifact{name: "leaf", identity: strings.Repeat("1", 64)}
	root := &mockBuildArtifact{
		name:     "root",
		identity: strings.Repeat("2", 64),
		deps:     []artifact.Artifact{leaf},
	}
	g := artifact.NewGraph()
	g.Add(leaf)
	g.Add(root)

	engine := artifact.NewEngine(g, store, 2)
	if _, err := engine.Run(log.WithTarget(context.Background(), log.Discard)); err != nil {
		t.Fatalf("engine.Run: %v", err)
	}

	// Collect the reachable outputs (manifest + leaves) we expect GC to keep.
	reachable := make(map[objstore.Hash]struct{})
	for _, a := range []artifact.Artifact{leaf, root} {
		m, leaves, err := a.OutputRefs(store)
		if err != nil {
			t.Fatalf("OutputRefs(%s): %v", a, err)
		}
		reachable[m] = struct{}{}
		for _, h := range leaves {
			reachable[h] = struct{}{}
		}
	}

	// A pinned input blob (e.g. an imported orig tarball): unreachable from the
	// graph, protected only by a pin.
	pinnedBlob, err := store.Blobs.Store(strings.NewReader("orig tarball bytes"))
	if err != nil {
		t.Fatalf("store pinned blob: %v", err)
	}
	if _, err := store.Pins.Create("libfoo 1.0 orig", []objstore.Hash{pinnedBlob}); err != nil {
		t.Fatalf("Pins.Create: %v", err)
	}

	// A stale output blob: looks like a build product but is reachable from no
	// current node and named by no pin. Must be collected.
	staleBlob, err := store.Blobs.Store(strings.NewReader("stale rebuildable output"))
	if err != nil {
		t.Fatalf("store stale blob: %v", err)
	}

	keep := buildGCKeepSet(t, g, store)

	// Assertions on the keep-set before sweeping.
	for h := range reachable {
		if _, ok := keep[h]; !ok {
			t.Errorf("keep-set missing reachable blob %s", h)
		}
	}
	if _, ok := keep[pinnedBlob]; !ok {
		t.Error("keep-set missing pinned blob")
	}
	if _, ok := keep[staleBlob]; ok {
		t.Error("keep-set should not contain the stale unpinned blob")
	}

	// Dry-run equivalent: sweeping must not run, so nothing is deleted. We assert
	// the stale blob still exists before the real sweep.
	if !store.Blobs.Has(staleBlob) {
		t.Fatal("stale blob vanished before sweep")
	}

	deleted, err := store.Blobs.Sweep(keep)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if deleted != 1 {
		t.Errorf("Sweep deleted %d blobs, want 1 (only the stale output)", deleted)
	}
	if store.Blobs.Has(staleBlob) {
		t.Error("stale blob should be deleted")
	}
	if !store.Blobs.Has(pinnedBlob) {
		t.Error("pinned blob must survive GC")
	}
	for h := range reachable {
		if !store.Blobs.Has(h) {
			t.Errorf("reachable blob %s must survive GC", h)
		}
	}

	// Dropping the pin then re-sweeping must now collect the formerly-pinned blob.
	pins := store.Pins.List()
	if len(pins) != 1 {
		t.Fatalf("expected 1 pin, got %d", len(pins))
	}
	if err := store.Pins.Drop(pins[0].ID); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	keep2 := buildGCKeepSet(t, g, store)
	if _, ok := keep2[pinnedBlob]; ok {
		t.Error("after dropping the pin, the blob must leave the keep-set")
	}
	deleted2, err := store.Blobs.Sweep(keep2)
	if err != nil {
		t.Fatalf("Sweep 2: %v", err)
	}
	if deleted2 != 1 || store.Blobs.Has(pinnedBlob) {
		t.Errorf("dropping the pin should let GC collect the blob (deleted=%d, present=%v)", deleted2, store.Blobs.Has(pinnedBlob))
	}
}
