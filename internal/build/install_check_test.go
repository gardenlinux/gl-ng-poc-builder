package build

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gl-ng/internal/debian/index"
	"gl-ng/internal/log"
	"gl-ng/internal/objstore"
)

// TestBuildLocalIndex verifies that buildLocalIndex loads packages from each
// dep's parent source-build manifest. This is the right path for siblings
// (whose own validation gate may not yet have run) and is also correct for
// cross-source deps (their parent source build is always validated first).
func TestBuildLocalIndex(t *testing.T) {
	storeDir := t.TempDir()
	store, err := objstore.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	// Create a fake control blob for "mylib"
	controlContent := "Package: mylib\nVersion: 1.0-1\nArchitecture: amd64\nDepends: libc6\n"
	controlHash, err := store.Blobs.Store(strings.NewReader(controlContent))
	if err != nil {
		t.Fatal(err)
	}

	// Create a fake .deb blob
	debContent := "fake-deb-content"
	debHash, err := store.Blobs.Store(strings.NewReader(debContent))
	if err != nil {
		t.Fatal(err)
	}

	// Create a source-build manifest using the stable-name convention emitted
	// by DebianPkgBuild.Build: "<debHash> mylib.deb\n<controlHash> control:mylib\n".
	binaryManifest := debHash.String() + " mylib.deb\n" + controlHash.String() + " control:mylib\n"
	binaryManifestHash, err := store.Blobs.Store(strings.NewReader(binaryManifest))
	if err != nil {
		t.Fatal(err)
	}

	// Create source package directories for "mylib" (needed for identity computation)
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "pkgs", "mylib")
	os.MkdirAll(filepath.Join(pkgDir, "src", "debian"), 0755)
	os.WriteFile(filepath.Join(pkgDir, "src", "debian", "control"),
		[]byte("Source: mylib\nPackage: mylib\nArchitecture: any\n"), 0644)
	os.WriteFile(filepath.Join(pkgDir, "sources.yml"), []byte("sources: []\n"), 0644)

	// Create the dep source build
	depSB := NewDebianPkgBuild(DebianPkgBuildConfig{
		Name:   "mylib",
		PkgDir: pkgDir,
		Arch:   "amd64",
		Store:  store,
	})
	depBP := depSB.Binary("mylib")

	// Store the source-build manifest under the parent source build's identity.
	// buildLocalIndex resolves via loadPackageFromSourceBuild — the parent source
	// build is always a Depends and therefore always available in the store, even
	// for sibling Includes whose own validation gate may not have run.
	srcID, err := depSB.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Map.Set(srcID, binaryManifestHash, false); err != nil {
		t.Fatal(err)
	}

	// Create source package for the test package "myapp"
	appDir := filepath.Join(dir, "pkgs", "myapp")
	os.MkdirAll(filepath.Join(appDir, "src", "debian"), 0755)
	os.WriteFile(filepath.Join(appDir, "src", "debian", "control"),
		[]byte("Source: myapp\nPackage: myapp\nArchitecture: any\n"), 0644)
	os.WriteFile(filepath.Join(appDir, "sources.yml"), []byte("sources: []\n"), 0644)

	appSB := NewDebianPkgBuild(DebianPkgBuildConfig{
		Name:   "myapp",
		PkgDir: appDir,
		Arch:   "amd64",
		Store:  store,
	})
	appBP := appSB.Binary("myapp")

	// Wire up: myapp extraDeps → [mylib]
	appBP.extraDeps = []*debianBinaryPkg{depBP}
	appBP.depsResolved = true

	// Run buildLocalIndex
	localIdx := appBP.buildLocalIndex(store)

	// Should find mylib (loaded from its parent source build's manifest)
	pkg := localIdx.Get("mylib")
	if pkg == nil {
		t.Fatal("expected mylib in local index (from source build manifest)")
	}
	if pkg.Version != "1.0-1" {
		t.Fatalf("expected version 1.0-1, got %s", pkg.Version)
	}
	if pkg.SHA256 != debHash.String() {
		t.Fatalf("expected SHA256=%s, got %s", debHash, pkg.SHA256)
	}

	// Self should NOT be in the index (it's the unvalidated package under test)
	if localIdx.Get("myapp") != nil {
		t.Fatal("myapp should NOT be in local index (it's the package under test)")
	}
}

// TestInstallCheckFailsWithoutLockfile verifies that the install check
// returns an error when no lockfile is available.
func TestInstallCheckFailsWithoutLockfile(t *testing.T) {
	storeDir := t.TempDir()
	store, err := objstore.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "pkgs", "nolockfile")
	os.MkdirAll(filepath.Join(pkgDir, "src", "debian"), 0755)
	os.WriteFile(filepath.Join(pkgDir, "src", "debian", "control"),
		[]byte("Source: nolockfile\nPackage: nolockfile\nArchitecture: any\n"), 0644)
	os.WriteFile(filepath.Join(pkgDir, "sources.yml"), []byte("sources: []\n"), 0644)
	// Intentionally NO build-deps.yml

	sb := NewDebianPkgBuild(DebianPkgBuildConfig{
		Name:   "nolockfile",
		PkgDir: pkgDir,
		Arch:   "amd64",
		Store:  store,
	})
	bp := sb.Binary("nolockfile")

	err = bp.installCheck(log.WithTarget(context.Background(), log.Discard), store, objstore.Hash{}, objstore.Hash{})
	if err == nil {
		t.Fatal("installCheck should fail without lockfile")
	}
	if !strings.Contains(err.Error(), "lockfile") {
		t.Fatalf("error should mention lockfile, got: %v", err)
	}
}

// TestMergedIndexOverridesLockfile verifies that local packages in the
// merged index properly override lockfile entries with the same name.
func TestMergedIndexOverridesLockfile(t *testing.T) {
	// Lockfile has mylib 1.0-1
	lockfileContent := strings.Join([]string{
		"Package: mylib",
		"Version: 1.0-1",
		"Architecture: amd64",
		"SHA256: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"",
	}, "\n")
	lockfileIndex, err := index.Load(strings.NewReader(lockfileContent))
	if err != nil {
		t.Fatal(err)
	}

	// Local build has mylib 1.0-1+gl~12345678
	localContent := strings.Join([]string{
		"Package: mylib",
		"Version: 1.0-1+gl~12345678",
		"Architecture: amd64",
		"SHA256: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"",
	}, "\n")
	localIndex, err := index.Load(strings.NewReader(localContent))
	if err != nil {
		t.Fatal(err)
	}

	merged := lockfileIndex.Merge(localIndex)
	pkg := merged.Get("mylib")
	if pkg == nil {
		t.Fatal("expected mylib in merged index")
	}
	if pkg.Version != "1.0-1+gl~12345678" {
		t.Fatalf("merged index should have local version, got %s", pkg.Version)
	}
	if pkg.SHA256 != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("merged index should have local SHA256, got %s", pkg.SHA256)
	}
}
