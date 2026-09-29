package build

import (
	"os"
	"path/filepath"
	"testing"

	"gl-ng/internal/buildcfg"
	"gl-ng/internal/objstore"
	"gopkg.in/yaml.v3"
)

func TestSourceBuildIdentityDeterministic(t *testing.T) {
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "pkgs", "testpkg")
	os.MkdirAll(filepath.Join(pkgDir, "src", "debian"), 0755)
	os.WriteFile(filepath.Join(pkgDir, "src", "debian", "control"), []byte("Source: testpkg\nBuild-Depends: debhelper\n"), 0644)
	os.WriteFile(filepath.Join(pkgDir, "sources.yml"), []byte("sources: []\n"), 0644)

	storeDir := t.TempDir()
	store, err := objstore.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	sb1 := NewDebianPkgBuild(DebianPkgBuildConfig{
		Name:   "testpkg",
		PkgDir: pkgDir,
		Arch:   "amd64",
		Store:  store,
	})
	sb2 := NewDebianPkgBuild(DebianPkgBuildConfig{
		Name:   "testpkg",
		PkgDir: pkgDir,
		Arch:   "amd64",
		Store:  store,
	})

	id1, err := sb1.Identity()
	if err != nil {
		t.Fatal(err)
	}
	id2, err := sb2.Identity()
	if err != nil {
		t.Fatal(err)
	}

	if id1 != id2 {
		t.Fatalf("identity not deterministic: %s != %s", id1, id2)
	}
}

func TestSourceBuildIdentityChangesWithSource(t *testing.T) {
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "pkgs", "testpkg")
	os.MkdirAll(filepath.Join(pkgDir, "src", "debian"), 0755)
	os.WriteFile(filepath.Join(pkgDir, "src", "debian", "control"), []byte("Source: testpkg\n"), 0644)
	os.WriteFile(filepath.Join(pkgDir, "sources.yml"), []byte("sources: []\n"), 0644)

	storeDir := t.TempDir()
	store, err := objstore.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	sb1 := NewDebianPkgBuild(DebianPkgBuildConfig{
		Name:   "testpkg",
		PkgDir: pkgDir,
		Arch:   "amd64",
		Store:  store,
	})
	id1, err := sb1.Identity()
	if err != nil {
		t.Fatal(err)
	}

	os.WriteFile(filepath.Join(pkgDir, "src", "debian", "control"), []byte("Source: testpkg\nBuild-Depends: gcc\n"), 0644)

	sb2 := NewDebianPkgBuild(DebianPkgBuildConfig{
		Name:   "testpkg",
		PkgDir: pkgDir,
		Arch:   "amd64",
		Store:  store,
	})
	id2, err := sb2.Identity()
	if err != nil {
		t.Fatal(err)
	}

	if id1 == id2 {
		t.Fatal("identity should change when source changes")
	}
}

func TestSourceBuildIdentityChangesWithArch(t *testing.T) {
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "pkgs", "testpkg")
	os.MkdirAll(filepath.Join(pkgDir, "src", "debian"), 0755)
	os.WriteFile(filepath.Join(pkgDir, "src", "debian", "control"), []byte("Source: testpkg\n"), 0644)
	os.WriteFile(filepath.Join(pkgDir, "sources.yml"), []byte("sources: []\n"), 0644)

	storeDir := t.TempDir()
	store, err := objstore.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	sb1 := NewDebianPkgBuild(DebianPkgBuildConfig{
		Name:   "testpkg",
		PkgDir: pkgDir,
		Arch:   "amd64",
		Store:  store,
	})
	sb2 := NewDebianPkgBuild(DebianPkgBuildConfig{
		Name:   "testpkg",
		PkgDir: pkgDir,
		Arch:   "arm64",
		Store:  store,
	})

	id1, _ := sb1.Identity()
	id2, _ := sb2.Identity()

	if id1 == id2 {
		t.Fatal("identity should differ for different architectures")
	}
}

func TestBinaryPackageIdentity(t *testing.T) {
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "pkgs", "testpkg")
	os.MkdirAll(filepath.Join(pkgDir, "src", "debian"), 0755)
	os.WriteFile(filepath.Join(pkgDir, "src", "debian", "control"), []byte("Source: testpkg\n"), 0644)
	os.WriteFile(filepath.Join(pkgDir, "sources.yml"), []byte("sources: []\n"), 0644)

	storeDir := t.TempDir()
	store, err := objstore.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	sb := NewDebianPkgBuild(DebianPkgBuildConfig{
		Name:   "testpkg",
		PkgDir: pkgDir,
		Arch:   "amd64",
		Store:  store,
	})

	bp := sb.Binary("testpkg-bin")

	id, err := bp.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if id.IsZero() {
		t.Fatal("binary package identity should not be zero")
	}
}

func TestRootfsIdentity(t *testing.T) {
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "pkgs", "testpkg")
	os.MkdirAll(filepath.Join(pkgDir, "src", "debian"), 0755)
	os.WriteFile(filepath.Join(pkgDir, "src", "debian", "control"), []byte("Source: testpkg\n"), 0644)
	os.WriteFile(filepath.Join(pkgDir, "sources.yml"), []byte("sources: []\n"), 0644)

	storeDir := t.TempDir()
	store, err := objstore.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	sb := NewDebianPkgBuild(DebianPkgBuildConfig{
		Name:   "testpkg",
		PkgDir: pkgDir,
		Arch:   "amd64",
		Store:  store,
	})

	bp := sb.Binary("testpkg")

	rootfs := newRootfsDirect("test-image", "amd64", []*debianBinaryPkg{bp}, store)

	id, err := rootfs.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if id.IsZero() {
		t.Fatal("rootfs identity should not be zero")
	}
}

// TestRootfsDependsOnlyDirect verifies that Rootfs.Depends() returns ONLY the
// direct binary package deps, not the full transitive closure. The engine handles
// recursive scheduling automatically via Discover().
func TestRootfsDependsOnlyDirect(t *testing.T) {
	dir := t.TempDir()

	// Source A produces binary "a-bin" which has extraDeps pointing to b-bin
	pkgDirA := filepath.Join(dir, "pkgs", "a")
	os.MkdirAll(filepath.Join(pkgDirA, "src", "debian"), 0755)
	os.WriteFile(filepath.Join(pkgDirA, "src", "debian", "control"), []byte("Source: a\n"), 0644)
	os.WriteFile(filepath.Join(pkgDirA, "sources.yml"), []byte("sources: []\n"), 0644)

	// Source B produces binary "b-bin"
	pkgDirB := filepath.Join(dir, "pkgs", "b")
	os.MkdirAll(filepath.Join(pkgDirB, "src", "debian"), 0755)
	os.WriteFile(filepath.Join(pkgDirB, "src", "debian", "control"), []byte("Source: b\n"), 0644)
	os.WriteFile(filepath.Join(pkgDirB, "sources.yml"), []byte("sources: []\n"), 0644)

	storeDir := t.TempDir()
	store, err := objstore.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	sbA := NewDebianPkgBuild(DebianPkgBuildConfig{
		Name:   "a",
		PkgDir: pkgDirA,
		Arch:   "amd64",
		Store:  store,
	})
	sbB := NewDebianPkgBuild(DebianPkgBuildConfig{
		Name:   "b",
		PkgDir: pkgDirB,
		Arch:   "amd64",
		Store:  store,
	})

	aBin := sbA.Binary("a-bin")
	bBin := sbB.Binary("b-bin")

	// a-bin has an extraDep on b-bin (simulating build.yml depends)
	aBin.extraDeps = []*debianBinaryPkg{bBin}
	aBin.depsResolved = true

	// Rootfs has only a-bin as a direct dep
	rootfs := newRootfsDirect("test-image", "amd64", []*debianBinaryPkg{aBin}, store)

	// Depends() should return ONLY a-bin
	deps := rootfs.Depends()
	if len(deps) != 1 {
		t.Fatalf("expected Depends() to return 1 direct dep, got %d", len(deps))
	}
	if deps[0].Key() != aBin.Key() {
		t.Fatalf("expected direct dep to be a-bin, got %s", deps[0].Key())
	}

	// allBinaryDeps() (used by Inputs/Identity) should include both a-bin and b-bin
	allDeps := rootfs.allBinaryDeps()
	if len(allDeps) != 2 {
		t.Fatalf("expected allBinaryDeps() to return 2 (transitive), got %d", len(allDeps))
	}

	names := make(map[string]bool)
	for _, bp := range allDeps {
		names[bp.name] = true
	}
	if !names["a-bin"] || !names["b-bin"] {
		t.Fatalf("expected both a-bin and b-bin in allBinaryDeps, got %v", names)
	}
}

func TestRuntimeDependsPerBinary(t *testing.T) {
	dir := t.TempDir()
	storeDir := t.TempDir()
	store, err := objstore.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	// Source "lib" produces lib-core and lib-utils
	libDir := filepath.Join(dir, "pkgs", "lib")
	os.MkdirAll(filepath.Join(libDir, "src", "debian"), 0755)
	os.WriteFile(filepath.Join(libDir, "src", "debian", "control"),
		[]byte("Source: lib\n\nPackage: lib-core\nArchitecture: any\n\nPackage: lib-utils\nArchitecture: any\n"), 0644)
	os.WriteFile(filepath.Join(libDir, "sources.yml"), []byte("sources: []\n"), 0644)

	// Source "app" produces app-bin and app-tools
	// runtime_depends: app-bin depends on lib:lib-core, app-tools has no extra deps
	appDir := filepath.Join(dir, "pkgs", "app")
	os.MkdirAll(filepath.Join(appDir, "src", "debian"), 0755)
	os.WriteFile(filepath.Join(appDir, "src", "debian", "control"),
		[]byte("Source: app\n\nPackage: app-bin\nArchitecture: any\n\nPackage: app-tools\nArchitecture: any\n"), 0644)
	os.WriteFile(filepath.Join(appDir, "sources.yml"), []byte("sources: []\n"), 0644)
	os.WriteFile(filepath.Join(appDir, "build.yml"),
		[]byte("runtime_depends:\n  app-bin: [lib:lib-core]\n"), 0644)

	ps, err := NewPackageSet(filepath.Join(dir, "pkgs"), "amd64", store, "")
	if err != nil {
		t.Fatal(err)
	}

	// app-bin should have lib-core in its extraDeps
	appBin, _ := ps.Binary("app", "app-bin")
	appBin.resolveExtraDeps()
	if len(appBin.extraDeps) != 1 {
		t.Fatalf("app-bin should have 1 extraDep (lib-core), got %d", len(appBin.extraDeps))
	}
	if appBin.extraDeps[0].name != "lib-core" {
		t.Fatalf("app-bin extraDep should be lib-core, got %s", appBin.extraDeps[0].name)
	}

	// app-tools should have NO extraDeps (runtime_depends only specifies app-bin)
	appTools, _ := ps.Binary("app", "app-tools")
	appTools.resolveExtraDeps()
	if len(appTools.extraDeps) != 0 {
		t.Fatalf("app-tools should have 0 extraDeps, got %d", len(appTools.extraDeps))
	}
}

func TestRuntimeDependsParsing(t *testing.T) {
	content := `build_profiles: [nocheck]
depends: [glibc:libc6]
runtime_depends:
  libc6: [gcc-16:libgcc-s1, gcc-16:gcc-16-base, glibc:libc-gconv-modules-extra]
  libfoo: [bar:baz]
`
	var cfg buildcfg.BuildYML
	if err := yaml.Unmarshal([]byte(content), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}

	if len(cfg.RuntimeDepends) != 2 {
		t.Fatalf("expected 2 runtime_depends entries, got %d", len(cfg.RuntimeDepends))
	}

	libc6Deps := cfg.RuntimeDepends["libc6"]
	if len(libc6Deps) != 3 {
		t.Fatalf("libc6 should have 3 runtime deps, got %d: %v", len(libc6Deps), libc6Deps)
	}
	if libc6Deps[0] != "gcc-16:libgcc-s1" {
		t.Fatalf("first libc6 dep should be gcc-16:libgcc-s1, got %s", libc6Deps[0])
	}

	fooDeps := cfg.RuntimeDepends["libfoo"]
	if len(fooDeps) != 1 || fooDeps[0] != "bar:baz" {
		t.Fatalf("libfoo deps unexpected: %v", fooDeps)
	}

	// Depends should still be parsed normally
	if len(cfg.Depends) != 1 || cfg.Depends[0] != "glibc:libc6" {
		t.Fatalf("depends unexpected: %v", cfg.Depends)
	}
}
