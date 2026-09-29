package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gl-ng/internal/objstore"
)

// TestBinaryIdentityIncludesDepHashes verifies that the binary package identity
// changes when one of its extra dependencies is rebuilt (i.e., gets a different
// source identity). The identity MUST incorporate dependency hashes, not just
// dependency names — otherwise two different versions of a dependency would
// produce the same binary identity, breaking cache correctness.
func TestBinaryIdentityIncludesDepHashes(t *testing.T) {
	dir := t.TempDir()
	storeDir := t.TempDir()
	store, err := objstore.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	// Create "libfoo" source package (version 1)
	libfooDir := filepath.Join(dir, "pkgs", "libfoo")
	os.MkdirAll(filepath.Join(libfooDir, "src", "debian"), 0755)
	os.WriteFile(filepath.Join(libfooDir, "src", "debian", "control"),
		[]byte("Source: libfoo\nPackage: libfoo1\nArchitecture: any\n"), 0644)
	os.WriteFile(filepath.Join(libfooDir, "sources.yml"), []byte("sources: []\n"), 0644)

	// Create "myapp" source package
	myappDir := filepath.Join(dir, "pkgs", "myapp")
	os.MkdirAll(filepath.Join(myappDir, "src", "debian"), 0755)
	os.WriteFile(filepath.Join(myappDir, "src", "debian", "control"),
		[]byte("Source: myapp\nPackage: myapp-bin\nArchitecture: any\n"), 0644)
	os.WriteFile(filepath.Join(myappDir, "sources.yml"), []byte("sources: []\n"), 0644)
	// myapp depends on libfoo via runtime_depends
	os.WriteFile(filepath.Join(myappDir, "build.yml"),
		[]byte("runtime_depends:\n  myapp-bin: [\"libfoo:libfoo1\"]\n"), 0644)

	// Build package set
	ps, err := NewPackageSet(filepath.Join(dir, "pkgs"), "amd64", store, "")
	if err != nil {
		t.Fatal(err)
	}

	// Get binary package with dependency resolution
	bp, err := ps.Binary("myapp", "myapp-bin")
	if err != nil {
		t.Fatal(err)
	}

	id1, err := bp.Identity()
	if err != nil {
		t.Fatal(err)
	}

	// Now modify libfoo's source (simulating a source change / rebuild)
	os.WriteFile(filepath.Join(libfooDir, "src", "debian", "control"),
		[]byte("Source: libfoo\nPackage: libfoo1\nArchitecture: any\nBuild-Depends: gcc\n"), 0644)

	// Recreate package set to get fresh identity computation
	ps2, err := NewPackageSet(filepath.Join(dir, "pkgs"), "amd64", store, "")
	if err != nil {
		t.Fatal(err)
	}

	bp2, err := ps2.Binary("myapp", "myapp-bin")
	if err != nil {
		t.Fatal(err)
	}

	id2, err := bp2.Identity()
	if err != nil {
		t.Fatal(err)
	}

	// CRITICAL: The binary identity MUST change when a dependency's source changes.
	// If it doesn't, we serve stale cache — the most dangerous bug in a build system.
	if id1 == id2 {
		t.Fatalf("CACHE CORRECTNESS BUG: binary identity did NOT change when dependency source changed.\n"+
			"  id before dep change: %s\n"+
			"  id after dep change:  %s\n"+
			"  The Identity() method uses dep.name instead of dep.Identity() for extra dependencies.\n"+
			"  This means rebuilding a dependency does NOT invalidate downstream caches.",
			id1, id2)
	}
}

// TestBinaryIdentityStableWhenDepsUnchanged verifies that binary identity is
// deterministic when nothing changes.
func TestBinaryIdentityStableWhenDepsUnchanged(t *testing.T) {
	dir := t.TempDir()
	storeDir := t.TempDir()
	store, err := objstore.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	libDir := filepath.Join(dir, "pkgs", "lib")
	os.MkdirAll(filepath.Join(libDir, "src", "debian"), 0755)
	os.WriteFile(filepath.Join(libDir, "src", "debian", "control"),
		[]byte("Source: lib\nPackage: lib1\nArchitecture: any\n"), 0644)
	os.WriteFile(filepath.Join(libDir, "sources.yml"), []byte("sources: []\n"), 0644)

	appDir := filepath.Join(dir, "pkgs", "app")
	os.MkdirAll(filepath.Join(appDir, "src", "debian"), 0755)
	os.WriteFile(filepath.Join(appDir, "src", "debian", "control"),
		[]byte("Source: app\nPackage: app-bin\nArchitecture: any\n"), 0644)
	os.WriteFile(filepath.Join(appDir, "sources.yml"), []byte("sources: []\n"), 0644)
	os.WriteFile(filepath.Join(appDir, "build.yml"),
		[]byte("runtime_depends:\n  app-bin: [\"lib:lib1\"]\n"), 0644)

	ps1, _ := NewPackageSet(filepath.Join(dir, "pkgs"), "amd64", store, "")
	bp1, _ := ps1.Binary("app", "app-bin")
	id1, err := bp1.Identity()
	if err != nil {
		t.Fatal(err)
	}

	ps2, _ := NewPackageSet(filepath.Join(dir, "pkgs"), "amd64", store, "")
	bp2, _ := ps2.Binary("app", "app-bin")
	id2, err := bp2.Identity()
	if err != nil {
		t.Fatal(err)
	}

	if id1 != id2 {
		t.Fatalf("identity should be deterministic for unchanged inputs: %s != %s", id1, id2)
	}
}

// TestRootfsIdentityChangesWhenTransitiveDepChanges verifies that the rootfs
// identity incorporates the full transitive dependency closure. A change to
// any package in the closure must change the rootfs identity.
func TestRootfsIdentityChangesWhenTransitiveDepChanges(t *testing.T) {
	dir := t.TempDir()
	storeDir := t.TempDir()
	store, err := objstore.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	// lib (leaf dependency)
	libDir := filepath.Join(dir, "pkgs", "lib")
	os.MkdirAll(filepath.Join(libDir, "src", "debian"), 0755)
	os.WriteFile(filepath.Join(libDir, "src", "debian", "control"),
		[]byte("Source: lib\nPackage: lib1\nArchitecture: any\n"), 0644)
	os.WriteFile(filepath.Join(libDir, "sources.yml"), []byte("sources: []\n"), 0644)

	// app depends on lib
	appDir := filepath.Join(dir, "pkgs", "app")
	os.MkdirAll(filepath.Join(appDir, "src", "debian"), 0755)
	os.WriteFile(filepath.Join(appDir, "src", "debian", "control"),
		[]byte("Source: app\nPackage: app-bin\nArchitecture: any\n"), 0644)
	os.WriteFile(filepath.Join(appDir, "sources.yml"), []byte("sources: []\n"), 0644)
	os.WriteFile(filepath.Join(appDir, "build.yml"),
		[]byte("depends: [\"lib:lib1\"]\n"), 0644)

	ps, _ := NewPackageSet(filepath.Join(dir, "pkgs"), "amd64", store, "")
	bp, _ := ps.Binary("app", "app-bin")
	rootfs1 := newRootfsDirect("test", "amd64", []*debianBinaryPkg{bp}, store)
	id1, err := rootfs1.Identity()
	if err != nil {
		t.Fatal(err)
	}

	// Change the transitive dependency (lib)
	os.WriteFile(filepath.Join(libDir, "src", "debian", "control"),
		[]byte("Source: lib\nPackage: lib1\nArchitecture: any\nDescription: changed\n"), 0644)

	ps2, _ := NewPackageSet(filepath.Join(dir, "pkgs"), "amd64", store, "")
	bp2, _ := ps2.Binary("app", "app-bin")
	rootfs2 := newRootfsDirect("test", "amd64", []*debianBinaryPkg{bp2}, store)
	id2, err := rootfs2.Identity()
	if err != nil {
		t.Fatal(err)
	}

	if id1 == id2 {
		t.Fatalf("rootfs identity MUST change when a transitive dependency's source changes.\n"+
			"  id before: %s\n  id after: %s", id1, id2)
	}
}

// TestSourceBuildIdentityDependsOnSourcesYML verifies that the source build
// identity incorporates the sources.yml hashes (orig tarball references).
// Without this, swapping an orig tarball wouldn't invalidate the cache.
func TestSourceBuildIdentityDependsOnSourcesYML(t *testing.T) {
	dir := t.TempDir()
	storeDir := t.TempDir()
	store, err := objstore.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	pkgDir := filepath.Join(dir, "pkgs", "testpkg")
	os.MkdirAll(filepath.Join(pkgDir, "src", "debian"), 0755)
	os.WriteFile(filepath.Join(pkgDir, "src", "debian", "control"),
		[]byte("Source: testpkg\n"), 0644)

	// sources.yml with one hash
	os.WriteFile(filepath.Join(pkgDir, "sources.yml"), []byte(`sources:
  - name: "testpkg_1.0.orig.tar.xz"
    hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
`), 0644)

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

	// Change the orig tarball hash in sources.yml
	os.WriteFile(filepath.Join(pkgDir, "sources.yml"), []byte(`sources:
  - name: "testpkg_1.0.orig.tar.xz"
    hash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
`), 0644)

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
		t.Fatalf("source build identity MUST change when orig tarball hash changes.\n"+
			"  id before: %s\n  id after: %s\n"+
			"  Without this, rebuilding after fetching a different orig tarball would\n"+
			"  serve stale cached build outputs.", id1, id2)
	}
}

// TestComputeVersionFormat verifies that computeVersion produces the expected
// synthetic version format: <upstream>-<debian_rev>+gl~<8char_hash>
func TestComputeVersionFormat(t *testing.T) {
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "pkgs", "hello")
	os.MkdirAll(filepath.Join(pkgDir, "src", "debian"), 0755)
	os.WriteFile(filepath.Join(pkgDir, "src", "debian", "control"),
		[]byte("Source: hello\n"), 0644)
	os.WriteFile(filepath.Join(pkgDir, "src", "debian", "changelog"),
		[]byte("hello (2.10-3) unstable; urgency=medium\n\n  * Test\n\n -- Dev <d@d>  Mon, 01 Jan 2024 00:00:00 +0000\n"), 0644)
	os.WriteFile(filepath.Join(pkgDir, "sources.yml"), []byte("sources: []\n"), 0644)

	storeDir := t.TempDir()
	store, err := objstore.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	sb := NewDebianPkgBuild(DebianPkgBuildConfig{
		Name:   "hello",
		PkgDir: pkgDir,
		Arch:   "amd64",
		Store:  store,
	})

	ver, err := sb.computeVersion()
	if err != nil {
		t.Fatal(err)
	}

	// Must start with the base version from changelog
	if !strings.HasPrefix(ver, "2.10-3+gl~") {
		t.Fatalf("version %q does not start with '2.10-3+gl~'", ver)
	}

	// The +gl~ suffix should be followed by exactly 8 hex characters
	suffix := strings.TrimPrefix(ver, "2.10-3+gl~")
	if len(suffix) != 8 {
		t.Fatalf("gl hash suffix should be 8 chars, got %d: %q", len(suffix), suffix)
	}
	for _, c := range suffix {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("gl hash suffix should be hex, got char %q in %q", string(c), suffix)
		}
	}
}

// TestComputeVersionChangesWithSource verifies that modifying the source tree
// changes the gl~ hash suffix (since it's derived from dirhash of src/).
func TestComputeVersionChangesWithSource(t *testing.T) {
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "pkgs", "hello")
	os.MkdirAll(filepath.Join(pkgDir, "src", "debian"), 0755)
	os.WriteFile(filepath.Join(pkgDir, "src", "debian", "control"),
		[]byte("Source: hello\n"), 0644)
	os.WriteFile(filepath.Join(pkgDir, "src", "debian", "changelog"),
		[]byte("hello (1.0-1) unstable; urgency=medium\n\n  * Init\n\n -- D <d@d>  Mon, 01 Jan 2024 00:00:00 +0000\n"), 0644)
	os.WriteFile(filepath.Join(pkgDir, "sources.yml"), []byte("sources: []\n"), 0644)

	storeDir := t.TempDir()
	store, _ := objstore.Open(storeDir)

	sb := NewDebianPkgBuild(DebianPkgBuildConfig{
		Name: "hello", PkgDir: pkgDir, Arch: "amd64", Store: store,
	})
	v1, _ := sb.computeVersion()

	// Modify source
	os.WriteFile(filepath.Join(pkgDir, "src", "debian", "control"),
		[]byte("Source: hello\nBuild-Depends: gcc\n"), 0644)

	sb2 := NewDebianPkgBuild(DebianPkgBuildConfig{
		Name: "hello", PkgDir: pkgDir, Arch: "amd64", Store: store,
	})
	v2, _ := sb2.computeVersion()

	if v1 == v2 {
		t.Fatalf("version should change when source changes: %s == %s", v1, v2)
	}

	// Base version part should remain the same
	if !strings.HasPrefix(v1, "1.0-1+gl~") || !strings.HasPrefix(v2, "1.0-1+gl~") {
		t.Fatalf("both versions should keep base '1.0-1+gl~': v1=%s v2=%s", v1, v2)
	}
}

// TestParseChangelogTopEntry verifies extraction of source name, base version,
// and trailer date from the first entry of debian/changelog.
func TestParseChangelogTopEntry(t *testing.T) {
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "pkgs", "openssl")
	os.MkdirAll(filepath.Join(pkgDir, "src", "debian"), 0755)
	os.WriteFile(filepath.Join(pkgDir, "src", "debian", "changelog"),
		[]byte("openssl (3.2.1-1) unstable; urgency=medium\n\n  * New upstream.\n\n -- Foo <f@f>  Mon, 01 Jan 2024 12:34:56 +0000\n"),
		0644)
	os.WriteFile(filepath.Join(pkgDir, "sources.yml"), []byte("sources: []\n"), 0644)

	storeDir := t.TempDir()
	store, _ := objstore.Open(storeDir)
	sb := NewDebianPkgBuild(DebianPkgBuildConfig{
		Name: "openssl", PkgDir: pkgDir, Arch: "amd64", Store: store,
	})

	name, ver, date, err := sb.parseChangelogTopEntry()
	if err != nil {
		t.Fatal(err)
	}
	if name != "openssl" {
		t.Errorf("name = %q, want %q", name, "openssl")
	}
	if ver != "3.2.1-1" {
		t.Errorf("version = %q, want %q", ver, "3.2.1-1")
	}
	if date != "Mon, 01 Jan 2024 12:34:56 +0000" {
		t.Errorf("date = %q, want %q", date, "Mon, 01 Jan 2024 12:34:56 +0000")
	}
}

// TestParseChangelogTopEntryMalformed verifies clear errors on broken
// changelogs — the build_phases caller propagates these rather than
// silently masking them.
func TestParseChangelogTopEntryMalformed(t *testing.T) {
	cases := map[string]string{
		"no parens":              "not a real changelog\n",
		"no trailer":             "openssl (3.2.1-1) unstable; urgency=medium\n\n  * Body but no trailer.\n",
		"no date sep in trailer": "openssl (3.2.1-1) unstable; urgency=medium\n\n  * Body.\n\n -- BadTrailer\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := parseChangelogTopEntryBytes([]byte(body)); err == nil {
				t.Fatalf("expected error for %q, got nil", name)
			}
		})
	}
}

// TestFormatGlChangelogEntry pins the exact byte layout of the prepended
// changelog entry. The trailer date is mirrored from the existing top
// entry — a deterministic input — so the produced .deb stays
// reproducible across rebuilds of the same source.
func TestFormatGlChangelogEntry(t *testing.T) {
	got := formatGlChangelogEntry("openssl", "3.2.1-1+gl~deadbeef", "Mon, 01 Jan 2024 12:34:56 +0000")
	want := "openssl (3.2.1-1+gl~deadbeef) UNRELEASED; urgency=medium\n" +
		"\n" +
		"  * Build with gl-ng.\n" +
		"\n" +
		" -- nobody <nobody@localhost>  Mon, 01 Jan 2024 12:34:56 +0000\n" +
		"\n"
	if got != want {
		t.Errorf("entry mismatch:\n got: %q\nwant: %q", got, want)
	}
}
