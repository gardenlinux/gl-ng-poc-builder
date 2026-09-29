package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gl-ng/internal/objstore"
)

// requireBin returns the path to the pre-built `gl` binary via GL_GL_BIN.
// Per the project convention (CLAUDE.md), tests do not invoke `go build` —
// the Makefile builds the binary once and exports GL_GL_BIN before `go test`.
func requireBin(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("GL_GL_BIN")
	if bin == "" {
		t.Skip("GL_GL_BIN not set; run via `make test` (the Makefile sets it)")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("GL_GL_BIN=%q does not exist; run `make build` first", bin)
	}
	return bin
}

// run invokes gl with the given args, returning combined stdout+stderr and
// the exec error (typically *ExitError on non-zero exit).
func run(t *testing.T, env []string, args ...string) ([]byte, error) {
	t.Helper()
	bin := requireBin(t)
	cmd := exec.Command(bin, args...)
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	return cmd.CombinedOutput()
}

func TestCLINoArgsExitsNonZero(t *testing.T) {
	if _, err := run(t, nil); err == nil {
		t.Fatal("expected non-zero exit when invoked with no args")
	}
}

func TestCLIHelp(t *testing.T) {
	for _, arg := range []string{"help", "--help", "-h"} {
		t.Run(arg, func(t *testing.T) {
			out, err := run(t, nil, arg)
			if err != nil {
				t.Fatalf("gl %s: %v\n%s", arg, err, out)
			}
			if !strings.Contains(string(out), "usage:") {
				t.Errorf("expected 'usage:' in %s output, got: %s", arg, out)
			}
			// Should advertise the subcommands so users can discover them.
			for _, sub := range []string{"build", "cache", "import", "graph"} {
				if !strings.Contains(string(out), sub) {
					t.Errorf("help should mention %q, got:\n%s", sub, out)
				}
			}
		})
	}
}

func TestCLIUnknownCommand(t *testing.T) {
	out, err := run(t, nil, "totally-unknown-cmd")
	if err == nil {
		t.Fatal("expected error for unknown command")
	}
	if !strings.Contains(string(out), "unknown command") {
		t.Errorf("expected 'unknown command' in output, got: %s", out)
	}
}

func TestCLIStatus(t *testing.T) {
	out, err := run(t, nil, "status")
	if err != nil {
		t.Fatalf("gl status: %v\n%s", err, out)
	}
	if len(out) == 0 {
		t.Fatal("expected status output")
	}
}

func TestCLICacheStatusEmpty(t *testing.T) {
	dir := t.TempDir()
	out, err := run(t, []string{"GL_CACHE=" + dir}, "cache", "status")
	if err != nil {
		t.Fatalf("gl cache status: %v\n%s", err, out)
	}
	// Fresh cache should report zero blobs / map entries.
	if !strings.Contains(string(out), "blobs:") {
		t.Errorf("cache status output should mention blobs:\n%s", out)
	}
}

func TestCLICacheUnknownSubcommand(t *testing.T) {
	dir := t.TempDir()
	out, err := run(t, []string{"GL_CACHE=" + dir}, "cache", "bogus")
	if err == nil {
		t.Fatal("expected error for unknown cache subcommand")
	}
	if !strings.Contains(string(out), "unknown cache subcommand") {
		t.Errorf("expected 'unknown cache subcommand' in output, got:\n%s", out)
	}
}

func TestCLICacheNoSubcommand(t *testing.T) {
	dir := t.TempDir()
	out, err := run(t, []string{"GL_CACHE=" + dir}, "cache")
	if err == nil {
		t.Fatal("expected error when `gl cache` is called with no subcommand")
	}
	if !strings.Contains(string(out), "usage:") {
		t.Errorf("expected usage message, got:\n%s", out)
	}
}

func TestCLICacheBlobsStoreAndGet(t *testing.T) {
	dir := t.TempDir()
	env := []string{"GL_CACHE=" + dir}

	// Write a small file, store it, and verify we get a hash back.
	contentPath := filepath.Join(dir, "payload.txt")
	if err := os.WriteFile(contentPath, []byte("hello-cache-roundtrip\n"), 0644); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, env, "cache", "blobs", "store", contentPath)
	if err != nil {
		t.Fatalf("cache blobs store: %v\n%s", err, out)
	}
	hash := strings.TrimSpace(string(out))
	// SHA-256 hex is 64 chars.
	if len(hash) < 64 {
		t.Fatalf("expected hash output (>=64 hex chars), got %q", hash)
	}
	// Take the last whitespace-separated token in case logs are interleaved.
	fields := strings.Fields(hash)
	hash = fields[len(fields)-1]

	out, err = run(t, env, "cache", "blobs", "check", hash)
	if err != nil {
		t.Fatalf("cache blobs check %s: %v\n%s", hash, err, out)
	}

	out, err = run(t, env, "cache", "blobs", "get", hash)
	if err != nil {
		t.Fatalf("cache blobs get %s: %v\n%s", hash, err, out)
	}
	if !strings.Contains(string(out), "hello-cache-roundtrip") {
		t.Fatalf("expected stored content back from `cache blobs get`, got:\n%s", out)
	}
}

func TestCLICacheBlobsCheckMissing(t *testing.T) {
	dir := t.TempDir()
	env := []string{"GL_CACHE=" + dir}
	missing := strings.Repeat("0", 64)
	out, err := run(t, env, "cache", "blobs", "check", missing)
	if err != nil {
		t.Fatalf("cache blobs check (missing): %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "not found") {
		t.Errorf("expected 'not found' for missing blob, got:\n%s", out)
	}
}

func TestCLICacheBlobsStoreMissingFile(t *testing.T) {
	dir := t.TempDir()
	env := []string{"GL_CACHE=" + dir}
	if _, err := run(t, env, "cache", "blobs", "store", "/nonexistent/file"); err == nil {
		t.Fatal("expected error storing nonexistent file")
	}
}

func TestCLIImportRequiresArg(t *testing.T) {
	if _, err := run(t, nil, "import"); err == nil {
		t.Fatal("expected error when `gl import` is called with no args")
	}
}

// seedPin opens the store at dir directly and creates a pin, returning its id.
// Pins are normally auto-created by import/lockfile; the CLI only lists/shows/
// drops. Tests seed through the store to exercise the read/drop commands.
func seedPin(t *testing.T, dir, name string, blobs []string) string {
	t.Helper()
	store, err := objstore.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	hs := make([]objstore.Hash, 0, len(blobs))
	for _, content := range blobs {
		h, err := store.Blobs.Store(strings.NewReader(content))
		if err != nil {
			t.Fatalf("store blob: %v", err)
		}
		hs = append(hs, h)
	}
	id, err := store.Pins.Create(name, hs)
	if err != nil {
		t.Fatalf("Pins.Create: %v", err)
	}
	return id
}

func TestCLICachePinListShowDrop(t *testing.T) {
	requireBin(t)
	dir := t.TempDir()
	env := []string{"GL_CACHE=" + dir}

	id := seedPin(t, dir, "libfoo 1.2.3 orig", []string{"orig-a", "orig-b"})

	// list shows the pin id and name.
	out, err := run(t, env, "cache", "pin", "list")
	if err != nil {
		t.Fatalf("cache pin list: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), id) || !strings.Contains(string(out), "libfoo 1.2.3 orig") {
		t.Errorf("pin list should show id and name, got:\n%s", out)
	}

	// show lists the two blob hashes.
	out, err = run(t, env, "cache", "pin", "show", id)
	if err != nil {
		t.Fatalf("cache pin show: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "blobs (2)") {
		t.Errorf("pin show should report 2 blobs, got:\n%s", out)
	}

	// drop removes it and warns about irreversibility.
	out, err = run(t, env, "cache", "pin", "drop", id)
	if err != nil {
		t.Fatalf("cache pin drop: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "unrecoverable") {
		t.Errorf("drop should warn about unrecoverable blobs, got:\n%s", out)
	}

	// After drop, list is empty and show fails.
	out, _ = run(t, env, "cache", "pin", "list")
	if !strings.Contains(string(out), "(no pins)") {
		t.Errorf("after drop, list should be empty, got:\n%s", out)
	}
	if _, err := run(t, env, "cache", "pin", "show", id); err == nil {
		t.Error("show should fail for a dropped pin")
	}
}

func TestCLICachePinShowRejectsBadID(t *testing.T) {
	requireBin(t)
	dir := t.TempDir()
	env := []string{"GL_CACHE=" + dir}
	if _, err := run(t, env, "cache", "pin", "show", "not-a-pin"); err == nil {
		t.Error("expected error for malformed pin id")
	}
}
