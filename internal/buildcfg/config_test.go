package buildcfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadBuildYML_Simple(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "build.yml"), `build_profiles: [nocheck, noudeb]
`)
	cfg := LoadBuildYML(dir)
	if len(cfg.BuildProfiles) != 2 || cfg.BuildProfiles[0] != "nocheck" || cfg.BuildProfiles[1] != "noudeb" {
		t.Fatalf("BuildProfiles: %v", cfg.BuildProfiles)
	}
}

func TestLoadBuildYML_Full(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "build.yml"), `build_profiles: [nocheck, noudeb]
build_options: [nocheck]
extra_build_env:
  - "DEB_CFLAGS_APPEND=-Wno-error"
  - "OTHER=1"
depends: [glibc:libc6-dev, attr:libattr1-dev]
runtime_depends:
  libc6: [gcc-16:libgcc-s1, glibc:libc-gconv-modules-extra]
  libfoo: [bar:baz]
lockfile_deps:
  libc6: [linux-libc-dev, rpcsvc-proto]
`)
	cfg := LoadBuildYML(dir)
	if len(cfg.BuildProfiles) != 2 {
		t.Fatalf("BuildProfiles: %v", cfg.BuildProfiles)
	}
	if len(cfg.BuildOptions) != 1 || cfg.BuildOptions[0] != "nocheck" {
		t.Fatalf("BuildOptions: %v", cfg.BuildOptions)
	}
	if len(cfg.ExtraBuildEnv) != 2 || cfg.ExtraBuildEnv[0] != "DEB_CFLAGS_APPEND=-Wno-error" {
		t.Fatalf("ExtraBuildEnv: %v", cfg.ExtraBuildEnv)
	}
	if len(cfg.Depends) != 2 || cfg.Depends[1] != "attr:libattr1-dev" {
		t.Fatalf("Depends: %v", cfg.Depends)
	}
	if len(cfg.RuntimeDepends["libc6"]) != 2 {
		t.Fatalf("RuntimeDepends.libc6: %v", cfg.RuntimeDepends["libc6"])
	}
	if len(cfg.LockfileDeps["libc6"]) != 2 {
		t.Fatalf("LockfileDeps.libc6: %v", cfg.LockfileDeps["libc6"])
	}
}

func TestLoadBuildYML_Missing(t *testing.T) {
	dir := t.TempDir()
	cfg := LoadBuildYML(dir)
	if len(cfg.BuildProfiles) != 0 || len(cfg.Depends) != 0 {
		t.Fatalf("expected zero value for missing build.yml, got %+v", cfg)
	}
}

func TestLoadBuildYML_BlockProfiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "build.yml"), `build_profiles:
  - nocheck
  - noudeb
`)
	cfg := LoadBuildYML(dir)
	if len(cfg.BuildProfiles) != 2 || cfg.BuildProfiles[0] != "nocheck" {
		t.Fatalf("block-style profiles: %v", cfg.BuildProfiles)
	}
}

func TestLoadSourcesYML(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "sources.yml"), `sources:
  - name: "bash_5.3.orig.tar.xz"
    hash: "a70de6bb41f5e192534a5a1836b1d7fad9a8d4818a6e1506d70f38441552c17a"
  - name: "bash_5.3.orig.tar.xz.asc"
`)
	refs, err := LoadSourcesYML(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(refs))
	}
	if refs[0].Name != "bash_5.3.orig.tar.xz" {
		t.Fatalf("ref[0].Name: %s", refs[0].Name)
	}
	if refs[0].Hash.IsZero() {
		t.Fatalf("ref[0].Hash should be populated")
	}
	if !refs[1].Hash.IsZero() {
		t.Fatalf("ref[1].Hash should be zero (no hash in fixture)")
	}
}

func TestLoadRootfsYML(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "rootfs.yml"), `packages: [base-files:base-files, bash:bash, coreutils:coreutils]
`)
	pkgs := LoadRootfsYML(dir)
	if len(pkgs) != 3 || pkgs[0] != "base-files:base-files" || pkgs[2] != "coreutils:coreutils" {
		t.Fatalf("unexpected packages: %v", pkgs)
	}
}

func TestLoadRootfsYML_BlockList(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "rootfs.yml"), `packages:
  - base-files:base-files
  - bash:bash
`)
	pkgs := LoadRootfsYML(dir)
	if len(pkgs) != 2 {
		t.Fatalf("expected 2 packages, got %d: %v", len(pkgs), pkgs)
	}
}

func TestLoadArchHashYML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "build-deps.yml")
	writeFile(t, path, "amd64: 100228bd70ee0f2d5a7889527d0718dbdb890335338b6491336aae418a2e666d\n")

	h, err := LoadArchHashYML(path, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if h.String() != "100228bd70ee0f2d5a7889527d0718dbdb890335338b6491336aae418a2e666d" {
		t.Fatalf("hash: %s", h.String())
	}

	if _, err := LoadArchHashYML(path, "arm64"); err == nil {
		t.Fatalf("expected error for missing arch")
	}
}

func TestLoadArchHashYML_MissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadArchHashYML(filepath.Join(dir, "nope.yml"), "amd64")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadArchHashYML_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "build-deps.yml")
	// "{" is unterminated → YAML parse error.
	writeFile(t, path, "{not: valid: yaml")
	if _, err := LoadArchHashYML(path, "amd64"); err == nil {
		t.Fatal("expected YAML parse error")
	}
}

func TestLoadArchHashYML_InvalidHashHex(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "build-deps.yml")
	// Wrong length / non-hex content — objstore.NewHash should reject it.
	writeFile(t, path, "amd64: not-a-real-sha256\n")
	if _, err := LoadArchHashYML(path, "amd64"); err == nil {
		t.Fatal("expected error for invalid hash")
	}
}

func TestLoadArchHashYML_ErrorMessageMentionsArch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "build-deps.yml")
	writeFile(t, path, "amd64: 100228bd70ee0f2d5a7889527d0718dbdb890335338b6491336aae418a2e666d\n")
	_, err := LoadArchHashYML(path, "riscv64")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "riscv64") {
		t.Fatalf("error should mention requested arch, got: %v", err)
	}
}

// TestLoadBuildYML_MalformedTolerated documents the load-best-effort contract
// of LoadBuildYML: a malformed YAML file is treated like a missing one, so
// callers can `LoadBuildYML(dir)` unconditionally without ever needing to
// check for parse errors. If we ever switch to a strict loader, this test
// should be inverted to assert the error.
func TestLoadBuildYML_MalformedTolerated(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "build.yml"), "{this is: not: valid yaml")
	cfg := LoadBuildYML(dir)
	if cfg.BuildProfiles != nil || cfg.Depends != nil {
		t.Fatalf("expected zero value on malformed YAML, got %+v", cfg)
	}
}

func TestLoadSourcesYML_Missing(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadSourcesYML(dir)
	if err == nil {
		t.Fatal("expected error for missing sources.yml")
	}
}

func TestLoadSourcesYML_Malformed(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "sources.yml"), "sources: [not closed")
	if _, err := LoadSourcesYML(dir); err == nil {
		t.Fatal("expected YAML parse error")
	}
}

// TestLoadSourcesYML_InvalidHashSilentlyDropped documents that invalid hash
// strings in sources.yml currently produce a SourceRef with a zero hash
// rather than an error — config.go silently swallows the objstore.NewHash
// failure (config.go:78). This is a known sharp edge: callers must check
// Hash.IsZero() if they care. Pinned here so we notice if behavior drifts.
func TestLoadSourcesYML_InvalidHashSilentlyDropped(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "sources.yml"), `sources:
  - name: "x.tar.xz"
    hash: "not-a-real-hash"
`)
	refs, err := LoadSourcesYML(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if !refs[0].Hash.IsZero() {
		t.Fatalf("expected zero hash for invalid hash string, got %s", refs[0].Hash)
	}
}

func TestLoadRootfsYML_MissingTolerated(t *testing.T) {
	dir := t.TempDir()
	if pkgs := LoadRootfsYML(dir); pkgs != nil {
		t.Fatalf("expected nil on missing rootfs.yml, got %v", pkgs)
	}
}

func TestLoadRootfsYML_MalformedTolerated(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "rootfs.yml"), "packages: [not closed")
	if pkgs := LoadRootfsYML(dir); pkgs != nil {
		t.Fatalf("expected nil on malformed rootfs.yml, got %v", pkgs)
	}
}
