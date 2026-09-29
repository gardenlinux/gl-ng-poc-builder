package lockfile

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"gl-ng/internal/debian/index"
	"gl-ng/internal/objstore"
)

func TestWriteBuildDepsYML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "build-deps.yml")

	hash, _ := objstore.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err := writeBuildDepsYML(path, "amd64", hash); err != nil {
		t.Fatal(err)
	}

	data, err := readFile(t, path)
	if err != nil {
		t.Fatal(err)
	}

	expected := "amd64: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"
	if string(data) != expected {
		t.Fatalf("unexpected content:\n%s\nexpected:\n%s", string(data), expected)
	}
}

func TestWriteBuildDepsYMLCreatesParent(t *testing.T) {
	dir := t.TempDir()
	// Parent dir does not exist yet — writer must create it.
	path := filepath.Join(dir, "nested", "deeper", "build-deps.yml")
	hash, _ := objstore.NewHash("0000000000000000000000000000000000000000000000000000000000000001")
	if err := writeBuildDepsYML(path, "arm64", hash); err != nil {
		t.Fatal(err)
	}
	if _, err := readFile(t, path); err != nil {
		t.Fatalf("file should exist: %v", err)
	}
}

func TestDetectArchHostMatches(t *testing.T) {
	if _, err := exec.LookPath("dpkg"); err != nil {
		t.Skip("dpkg not present on host")
	}
	got := detectArchUncached()
	if got == "" {
		t.Fatalf("expected non-empty arch")
	}
}

func TestDetectArchFallback(t *testing.T) {
	t.Setenv("PATH", "/nonexistent")
	if got := detectArchUncached(); got != "amd64" {
		t.Fatalf("expected fallback amd64, got %q", got)
	}
}

func TestCapitalizeField(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"package", "Package"},
		{"build-depends", "Build-Depends"},
		{"pre-depends", "Pre-Depends"},
		{"sha256", "Sha256"},
		{"", ""},
	}
	for _, tt := range tests {
		got := capitalizeField(tt.in)
		if got != tt.want {
			t.Errorf("capitalizeField(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestExtractBuildDeps(t *testing.T) {
	dir := t.TempDir()
	controlPath := filepath.Join(dir, "control")
	writeFile(t, controlPath, `Source: mypackage
Section: libs
Build-Depends: debhelper (>= 13), libfoo-dev, pkg-config
Build-Depends-Arch: libc6-dev

Package: mypackage
Architecture: any
`)

	deps, err := extractBuildDeps(controlPath)
	if err != nil {
		t.Fatal(err)
	}

	if len(deps) < 3 {
		t.Fatalf("expected at least 3 deps, got %d", len(deps))
	}
}

func TestExtractBuildDepsMergesArchAndIndep(t *testing.T) {
	dir := t.TempDir()
	controlPath := filepath.Join(dir, "control")
	writeFile(t, controlPath, `Source: mypackage
Build-Depends: a
Build-Depends-Arch: b
Build-Depends-Indep: c

Package: mypackage
Architecture: any
`)
	deps, err := extractBuildDeps(controlPath)
	if err != nil {
		t.Fatal(err)
	}
	// Each Build-Depends* contributes one alternative; the parser concatenates
	// them with ", " so we should see exactly three top-level alternatives.
	if len(deps) != 3 {
		t.Fatalf("expected 3 alternatives (a,b,c), got %d: %#v", len(deps), deps)
	}
}

func TestExtractBuildDepsMissingFile(t *testing.T) {
	if _, err := extractBuildDeps("/nonexistent/control"); err == nil {
		t.Fatal("expected error for missing control file")
	}
}

func TestExtractBuildDepsNoFields(t *testing.T) {
	// A control file with no Build-Depends* at all should parse cleanly and
	// return a nil DependencyList — not an error.
	dir := t.TempDir()
	controlPath := filepath.Join(dir, "control")
	writeFile(t, controlPath, `Source: bare
Section: libs

Package: bare
Architecture: any
`)
	deps, err := extractBuildDeps(controlPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deps) != 0 {
		t.Fatalf("expected no deps, got %d", len(deps))
	}
}

// =============================================================================
// serializeIndex determinism
// =============================================================================

func TestSerializeIndexDeterministic(t *testing.T) {
	pkgs := []*index.Package{
		makePackage("alpha", "1.0", map[string]string{
			"package":      "alpha",
			"version":      "1.0",
			"architecture": "amd64",
			"depends":      "libc6",
			"sha256":       "aaaa",
			"filename":     "pool/a.deb",
			"size":         "100",
		}),
		makePackage("beta", "2.0", map[string]string{
			"package":      "beta",
			"version":      "2.0",
			"architecture": "amd64",
			"sha256":       "bbbb",
		}),
	}

	first := serializeIndex(pkgs)
	for i := 0; i < 5; i++ {
		got := serializeIndex(pkgs)
		if got != first {
			t.Fatalf("serializeIndex non-deterministic on iter %d:\n--- first ---\n%s--- iter ---\n%s", i, first, got)
		}
	}
}

func TestSerializeIndexDropsLocalFields(t *testing.T) {
	pkgs := []*index.Package{
		makePackage("alpha", "1.0", map[string]string{
			"package":  "alpha",
			"version":  "1.0",
			"filename": "pool/a/alpha.deb",
			"size":     "12345",
			"sha256":   "abc",
		}),
	}
	got := serializeIndex(pkgs)
	if strings.Contains(got, "Filename:") {
		t.Errorf("Filename should be stripped:\n%s", got)
	}
	if strings.Contains(got, "Size:") {
		t.Errorf("Size should be stripped:\n%s", got)
	}
	// SHA256, Package, Version must be preserved.
	for _, must := range []string{"Package: alpha", "Version: 1.0", "Sha256: abc"} {
		if !strings.Contains(got, must) {
			t.Errorf("expected %q in output:\n%s", must, got)
		}
	}
}

func TestSerializeIndexAlphabeticalKeys(t *testing.T) {
	// Stanza key iteration in Go is randomised; serializeIndex must impose a
	// stable order. Verify the output keys come out alphabetically.
	pkgs := []*index.Package{
		makePackage("zeta", "1.0", map[string]string{
			"package":      "zeta",
			"architecture": "amd64",
			"depends":      "libc6",
			"version":      "1.0",
			"sha256":       "fff",
			"description":  "z package",
		}),
	}
	out := serializeIndex(pkgs)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// Each line is "Key: value"; extract keys and verify sorted order.
	var keys []string
	for _, line := range lines {
		if idx := strings.Index(line, ":"); idx > 0 {
			keys = append(keys, strings.ToLower(line[:idx]))
		}
	}
	for i := 1; i < len(keys); i++ {
		if keys[i] < keys[i-1] {
			t.Fatalf("keys not sorted: %v", keys)
		}
	}
}

func TestSerializeIndexEmpty(t *testing.T) {
	if got := serializeIndex(nil); got != "" {
		t.Errorf("expected empty output for nil packages, got %q", got)
	}
}

// =============================================================================
// applyDefaults
// =============================================================================

func TestConfigApplyDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()

	if cfg.RepoURL != "https://deb.debian.org/debian" {
		t.Errorf("RepoURL: %q", cfg.RepoURL)
	}
	if cfg.Dist != "testing" {
		t.Errorf("Dist: %q", cfg.Dist)
	}
	if cfg.Arch == "" {
		t.Errorf("Arch should be detected, got empty")
	}
	if cfg.Ctx == nil {
		t.Errorf("Ctx should default to background")
	}
}

func TestConfigApplyDefaultsRespectsExplicit(t *testing.T) {
	ctx := context.WithValue(context.Background(), struct{}{}, "x")
	cfg := &Config{
		RepoURL: "http://mirror.local/debian",
		Dist:    "bookworm",
		Arch:    "arm64",
		Ctx:     ctx,
	}
	cfg.applyDefaults()
	if cfg.RepoURL != "http://mirror.local/debian" {
		t.Errorf("RepoURL overwritten: %q", cfg.RepoURL)
	}
	if cfg.Dist != "bookworm" {
		t.Errorf("Dist overwritten: %q", cfg.Dist)
	}
	if cfg.Arch != "arm64" {
		t.Errorf("Arch overwritten: %q", cfg.Arch)
	}
	if cfg.Ctx != ctx {
		t.Errorf("Ctx overwritten")
	}
}

func TestRootfsConfigApplyDefaults(t *testing.T) {
	cfg := &RootfsConfig{}
	cfg.applyDefaults()
	if cfg.RepoURL == "" || cfg.Dist == "" || cfg.Arch == "" {
		t.Errorf("defaults not applied: %+v", cfg)
	}
}

// =============================================================================
// buildResolverRoots / buildRootfsRoots
// =============================================================================

func TestBuildRootfsRootsIncludesPostinstInfra(t *testing.T) {
	idx := makeIndex(t, []map[string]string{
		{"package": "libc6", "version": "1", "essential": "yes"},
		{"package": "dash", "version": "1"},
	})
	roots := buildRootfsRoots(idx)
	names := make(map[string]bool)
	for _, r := range roots {
		names[r.Name] = true
	}
	for _, want := range []string{"libc6", "perl-base", "mawk"} {
		if !names[want] {
			t.Errorf("buildRootfsRoots missing %q: %v", want, names)
		}
	}
	if names["apt"] {
		t.Errorf("buildRootfsRoots should not include apt — rootfs assembly is dpkg-only")
	}
}

func TestBuildResolverRootsIncludesEssentialAndImplicit(t *testing.T) {
	idx := makeIndex(t, []map[string]string{
		{"package": "libc6", "version": "1", "essential": "yes"},
	})
	roots := buildResolverRoots(nil, idx, "amd64", nil)
	names := make(map[string]bool)
	for _, r := range roots {
		names[r.Name] = true
	}
	for _, want := range []string{"libc6", "build-essential", "fakeroot", "debconf"} {
		if !names[want] {
			t.Errorf("missing %q in roots: %v", want, names)
		}
	}
}

// =============================================================================
// FetchDebs concurrency, hash validation, error semantics
// =============================================================================

func TestFetchDebsHappyPath(t *testing.T) {
	pkgs, srv := serveFakeDebs(t, map[string][]byte{
		"pool/a.deb": []byte("aaa"),
		"pool/b.deb": []byte("bbb"),
		"pool/c.deb": []byte("ccc"),
	})
	defer srv.Close()

	store, err := objstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if err := FetchDebs(context.Background(), store, srv.URL, pkgs); err != nil {
		t.Fatalf("FetchDebs: %v", err)
	}

	for _, p := range pkgs {
		h, _ := objstore.NewHash(p.SHA256)
		if !store.Blobs.Has(h) {
			t.Errorf("blob missing for %s", p.Name)
		}
	}
}

func TestFetchDebsSkipsAlreadyCached(t *testing.T) {
	pkgs, srv := serveFakeDebs(t, map[string][]byte{
		"pool/a.deb": []byte("preexisting-content"),
	})
	defer srv.Close()

	store, err := objstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Pre-store the blob with the same content the server would return.
	if _, err := store.Blobs.Store(strings.NewReader("preexisting-content")); err != nil {
		t.Fatal(err)
	}

	hits := atomic.Int32{}
	wrappedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r) // would fail download if reached
	}))
	defer wrappedSrv.Close()

	if err := FetchDebs(context.Background(), store, wrappedSrv.URL, pkgs); err != nil {
		t.Fatalf("FetchDebs: %v", err)
	}
	if hits.Load() != 0 {
		t.Errorf("server hit %d times, expected 0 (already cached)", hits.Load())
	}
}

func TestFetchDebsHashMismatch(t *testing.T) {
	// Server returns content whose hash does NOT match what we declared in the
	// package metadata. FetchDebs must detect this and refuse to store the blob.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("wrong-content"))
	}))
	defer srv.Close()

	pkgs := []*index.Package{
		{
			Name:     "evil",
			Filename: "pool/e.deb",
			SHA256:   sha256Hex([]byte("expected-content")),
		},
	}

	store, err := objstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	err = FetchDebs(context.Background(), store, srv.URL, pkgs)
	if err == nil {
		t.Fatal("expected hash-mismatch error")
	}
	if !strings.Contains(err.Error(), "hash mismatch") {
		t.Errorf("error should mention hash mismatch, got: %v", err)
	}
	// Wrong-hash content must NOT have been stored.
	wrongHash, _ := objstore.NewHash(sha256Hex([]byte("wrong-content")))
	if store.Blobs.Has(wrongHash) {
		t.Errorf("FetchDebs stored mismatched content — security regression")
	}
}

func TestFetchDebsHTTP404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	pkgs := []*index.Package{
		{Name: "missing", Filename: "pool/m.deb", SHA256: sha256Hex([]byte("x"))},
	}

	store, err := objstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	err = FetchDebs(context.Background(), store, srv.URL, pkgs)
	if err == nil {
		t.Fatal("expected error on 404")
	}
}

func TestFetchDebsSkipsPackageWithEmptySHA(t *testing.T) {
	// A package with no SHA256 is silently skipped (this is how virtual /
	// metapackage entries flow through). Document the contract.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server hit for SHA-less package — should have been skipped")
	}))
	defer srv.Close()

	store, err := objstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := FetchDebs(context.Background(), store, srv.URL, []*index.Package{
		{Name: "virtual", SHA256: ""},
	}); err != nil {
		t.Fatalf("FetchDebs should not error on empty SHA: %v", err)
	}
}

func TestFetchDebsConcurrentBatch(t *testing.T) {
	// Throw 50 packages at FetchDebs to exercise the 16-goroutine semaphore.
	contentByPath := make(map[string][]byte, 50)
	for i := 0; i < 50; i++ {
		contentByPath[fmt.Sprintf("pool/p%02d.deb", i)] = []byte(fmt.Sprintf("content-%d", i))
	}
	pkgs, srv := serveFakeDebs(t, contentByPath)
	defer srv.Close()

	store, err := objstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := FetchDebs(context.Background(), store, srv.URL, pkgs); err != nil {
		t.Fatalf("FetchDebs: %v", err)
	}
	for _, p := range pkgs {
		h, _ := objstore.NewHash(p.SHA256)
		if !store.Blobs.Has(h) {
			t.Errorf("blob missing for %s", p.Name)
		}
	}
}

// =============================================================================
// Helpers
// =============================================================================

func makePackage(name, version string, stanza map[string]string) *index.Package {
	pkg := &index.Package{
		Name:    name,
		Version: version,
		Stanza:  make(map[string]string, len(stanza)),
	}
	for k, v := range stanza {
		pkg.Stanza[k] = v
	}
	return pkg
}

func makeIndex(t *testing.T, stanzas []map[string]string) *index.Index {
	t.Helper()
	var b strings.Builder
	for _, s := range stanzas {
		// Package field must come first by convention (deb822 is order-tolerant
		// but readable fixtures help when tests fail).
		fmt.Fprintf(&b, "Package: %s\n", s["package"])
		for k, v := range s {
			if k == "package" {
				continue
			}
			fmt.Fprintf(&b, "%s: %s\n", strings.Title(k), v) //nolint:staticcheck
		}
		b.WriteString("\n")
	}
	idx, err := index.Load(strings.NewReader(b.String()))
	if err != nil {
		t.Fatalf("makeIndex: %v", err)
	}
	return idx
}

// serveFakeDebs spins up an httptest server that serves `pathToContent` and
// returns matching index.Package entries with correct SHA256 hashes.
func serveFakeDebs(t *testing.T, pathToContent map[string][]byte) ([]*index.Package, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		content, ok := pathToContent[path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(content)
	}))
	var pkgs []*index.Package
	i := 0
	for path, content := range pathToContent {
		pkgs = append(pkgs, &index.Package{
			Name:     fmt.Sprintf("pkg%d", i),
			Filename: path,
			SHA256:   sha256Hex(content),
		})
		i++
	}
	return pkgs, srv
}

func sha256HexHelper(data []byte) string {
	return sha256Hex(data)
}

// readFile is a thin wrapper that exists to keep tests independent of os.ReadFile.
func readFile(t *testing.T, path string) ([]byte, error) {
	t.Helper()
	return os.ReadFile(path)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
