package build

// e2e_synthetic_test.go covers gl-ng's metadata-driven validation paths using
// hand-crafted dummy packages from testdata/synthetic/. These tests do NOT
// require the network, the exec-env stub, user namespaces, or any heavy
// dpkg-buildpackage work — they exercise the parts of internal/build that
// drive correctness BEFORE any chroot is touched.
//
// What's tested here:
//   1. Cross-package runtime_depends wiring resolves to extraDeps
//      (TestSyntheticCrossPackageDeps).
//   2. Same-source runtime_depends wiring resolves to includes (siblings)
//      rather than extraDeps, so the graph engine sees no fake build-order
//      cycle (TestSyntheticMultiBinIncludes).
//   3. validateLocality rejects a binary whose Depends cannot be satisfied
//      from the local build set or lockfile_deps (TestSyntheticBrokenDeps).
//
// Heavier behaviors (real dpkg-buildpackage, container bootstrap, full
// installCheck) are exercised by TestHelloEndToEnd in e2e_hello_test.go.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gl-ng/internal/artifact"
	"gl-ng/internal/log"
	"gl-ng/internal/objstore"
)

// stageSynthetic copies a synthetic package tree from testdata/synthetic/<name>/
// into <stagingDir>/pkgs/<name>/ so NewPackageSet can find it. Returns the
// staging dir's pkgs/ path.
func stageSynthetic(t *testing.T, stagingDir string, names ...string) string {
	t.Helper()
	pkgsDir := filepath.Join(stagingDir, "pkgs")
	if err := os.MkdirAll(pkgsDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		src := filepath.Join("testdata", "synthetic", name)
		dst := filepath.Join(pkgsDir, name)
		if err := copyTree(src, dst); err != nil {
			t.Fatalf("stage %s: %v", name, err)
		}
	}
	return pkgsDir
}

// copyTree is a small recursive copy helper. Tests need it to stage the
// read-only testdata fixtures into a writable per-test directory; pulling in
// a tar/cp dependency would be overkill.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}

// TestSyntheticEmptyPkg is the trivial smoke test: a single binary with no
// Depends and no runtime_depends should construct cleanly via NewPackageSet
// and report an empty extraDeps / includes set. If this ever breaks, every
// other synthetic test will too — running this one first localizes the bug.
func TestSyntheticEmptyPkg(t *testing.T) {
	storeDir := t.TempDir()
	store, err := objstore.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	stagingDir := t.TempDir()
	pkgsDir := stageSynthetic(t, stagingDir, "empty-pkg")

	ps, err := NewPackageSet(pkgsDir, "amd64", store, "")
	if err != nil {
		t.Fatalf("NewPackageSet: %v", err)
	}

	bp, err := ps.Binary("empty-pkg", "empty-pkg")
	if err != nil {
		t.Fatalf("look up empty-pkg: %v", err)
	}
	bp.resolveExtraDeps()
	if len(bp.extraDeps) != 0 {
		t.Errorf("expected 0 extraDeps for empty-pkg, got %d", len(bp.extraDeps))
	}
	if len(bp.includes) != 0 {
		t.Errorf("expected 0 includes for empty-pkg, got %d", len(bp.includes))
	}
	if !ps.LocalSet()["empty-pkg"] {
		t.Errorf("LocalSet should contain empty-pkg: %v", ps.LocalSet())
	}
}

func TestSyntheticCrossPackageDeps(t *testing.T) {
	storeDir := t.TempDir()
	store, err := objstore.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	stagingDir := t.TempDir()
	pkgsDir := stageSynthetic(t, stagingDir, "lib-foo", "app-needs-foo")

	ps, err := NewPackageSet(pkgsDir, "amd64", store, "")
	if err != nil {
		t.Fatalf("NewPackageSet: %v", err)
	}

	appFoo, err := ps.Binary("app-needs-foo", "app-foo")
	if err != nil {
		t.Fatalf("look up app-foo: %v", err)
	}
	appFoo.resolveExtraDeps()

	// runtime_depends declares lib-foo:libfoo1, which is in a DIFFERENT source
	// package, so it must surface as an extraDep (cross-source build edge),
	// not an Include (sibling).
	if len(appFoo.extraDeps) != 1 {
		t.Fatalf("app-foo should have 1 extraDep (libfoo1 from lib-foo), got %d", len(appFoo.extraDeps))
	}
	if appFoo.extraDeps[0].name != "libfoo1" {
		t.Errorf("extraDep should be libfoo1, got %s", appFoo.extraDeps[0].name)
	}
	if len(appFoo.includes) != 0 {
		t.Errorf("app-foo should have 0 sibling includes, got %d", len(appFoo.includes))
	}

	// localSet must contain libfoo1 so validateLocality would be satisfied.
	localSet := appFoo.buildLocalSet()
	if !localSet["libfoo1"] {
		t.Errorf("localSet missing libfoo1 (cross-package dep): %v", localSet)
	}
}

func TestSyntheticMultiBinIncludes(t *testing.T) {
	storeDir := t.TempDir()
	store, err := objstore.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	stagingDir := t.TempDir()
	pkgsDir := stageSynthetic(t, stagingDir, "multi-bin")

	ps, err := NewPackageSet(pkgsDir, "amd64", store, "")
	if err != nil {
		t.Fatalf("NewPackageSet: %v", err)
	}

	tools, err := ps.Binary("multi-bin", "multi-bin-tools")
	if err != nil {
		t.Fatalf("look up multi-bin-tools: %v", err)
	}
	tools.resolveExtraDeps()

	// runtime_depends references multi-bin:multi-bin-shared — same source —
	// so it must be a sibling Include, not an extraDep. This is the rule that
	// keeps the graph acyclic: same-source siblings are co-produced and the
	// shared parent source build is the actual build-order edge.
	if len(tools.extraDeps) != 0 {
		t.Errorf("multi-bin-tools should have 0 extraDeps (sibling lives in same source), got %d", len(tools.extraDeps))
	}
	if len(tools.includes) != 1 {
		t.Fatalf("multi-bin-tools should have 1 sibling Include, got %d", len(tools.includes))
	}
	if tools.includes[0].name != "multi-bin-shared" {
		t.Errorf("Include should be multi-bin-shared, got %s", tools.includes[0].name)
	}

	// And the include must show up in the locality closure.
	localSet := tools.buildLocalSet()
	if !localSet["multi-bin-shared"] {
		t.Errorf("localSet missing multi-bin-shared (sibling include): %v", localSet)
	}
}

func TestSyntheticBrokenDeps(t *testing.T) {
	storeDir := t.TempDir()
	store, err := objstore.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	stagingDir := t.TempDir()
	pkgsDir := stageSynthetic(t, stagingDir, "app-broken-deps")

	ps, err := NewPackageSet(pkgsDir, "amd64", store, "")
	if err != nil {
		t.Fatalf("NewPackageSet: %v", err)
	}

	bp, err := ps.Binary("app-broken-deps", "app-broken")
	if err != nil {
		t.Fatalf("look up app-broken: %v", err)
	}

	// Hand-craft the inputs that the source build would normally produce:
	//   - control:app-broken — the control stanza with the unsatisfiable Depends
	//   - app-broken.deb     — placeholder bytes (validateLocality only reads control)
	// We invoke Build() rather than validateLocality directly so the test
	// pins the public surface: a graph engine running this artifact would
	// hit the same error path.
	controlStanza := "Package: app-broken\n" +
		"Version: 1.0-1\n" +
		"Architecture: amd64\n" +
		"Depends: this-package-does-not-exist\n"
	controlHash, err := store.Blobs.Store(strings.NewReader(controlStanza))
	if err != nil {
		t.Fatal(err)
	}
	debHash, err := store.Blobs.Store(strings.NewReader("placeholder-deb-bytes"))
	if err != nil {
		t.Fatal(err)
	}

	ctx := log.WithTarget(context.Background(), log.Discard)
	_, err = bp.Build(artifact.BuildContext{
		Ctx:   ctx,
		Store: store,
		Inputs: map[string]objstore.Hash{
			"app-broken.deb":     debHash,
			"control:app-broken": controlHash,
		},
	})
	if err == nil {
		t.Fatal("expected validateLocality to reject app-broken with unsatisfied dep, got nil error")
	}
	if !strings.Contains(err.Error(), "locality check failed") {
		t.Errorf("expected 'locality check failed' in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "this-package-does-not-exist") {
		t.Errorf("expected error to name the unsatisfied dep 'this-package-does-not-exist', got: %v", err)
	}
}

// Sanity check: the testdata fixtures themselves are well-formed (control
// files parse, every binary stanza yields a Package: line). A compile-time
// typo in a fixture would otherwise break TestSynthetic* tests in confusing
// ways far from the cause.
func TestSyntheticFixturesParse(t *testing.T) {
	for _, name := range []string{"empty-pkg", "lib-foo", "app-needs-foo", "multi-bin", "app-broken-deps"} {
		t.Run(name, func(t *testing.T) {
			pkgDir := filepath.Join("testdata", "synthetic", name)
			if _, err := os.Stat(filepath.Join(pkgDir, "src", "debian", "control")); err != nil {
				t.Fatalf("missing control: %v", err)
			}
			binNames := ParseBinaryPackageNames(pkgDir)
			if len(binNames) == 0 {
				t.Fatalf("no Package: stanzas parsed from %s", name)
			}
			for _, b := range binNames {
				if b == "" {
					t.Fatalf("empty binary name in %s", name)
				}
			}
		})
	}
}
