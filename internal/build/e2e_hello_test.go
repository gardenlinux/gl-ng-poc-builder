package build

// e2e_hello_test.go drives the full Phase-1 pipeline against the upstream
// `hello` Debian source package: source import → lockfile generation →
// DebianPkgBuild.Build (real dpkg-buildpackage in a chroot) →
// debianBinaryPkg.Build (locality validation + installCheck in a freshly
// bootstrapped container).
//
// `hello` is chosen because it's tiny (Depends: libc6 only), pure C, and
// historically the simplest non-trivial Debian package. To avoid having to
// build libc6 locally, we set lockfile_deps: { hello: [libc6] } in build.yml
// before the binary validation step — that lets the locality gate pass while
// still exercising the full installCheck container path.
//
// Subtests share the parent test's filesystem state by design: each one
// builds on the previous one's outputs. They are NOT t.Parallel() and the
// order is significant.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"gl-ng/internal/artifact"
	"gl-ng/internal/buildcfg"
	"gl-ng/internal/container"
	"gl-ng/internal/importer"
	"gl-ng/internal/lockfile"
	"gl-ng/internal/log"
	"gl-ng/internal/objstore"
)

// hasUserNS probes whether this host can create user namespaces. Many CI
// hosts and hardened workstations disable kernel.unprivileged_userns_clone or
// otherwise block unshare(); on those, source-build and installCheck cannot
// run at all and the test skips rather than failing.
func hasUserNS(t *testing.T, ctx context.Context, stubPath string) bool {
	t.Helper()
	base := container.NewBaseExecEnv()
	defer base.Close()
	uns, err := container.NewUserNS(container.UserNSConfig{
		Ctx:      ctx,
		Parent:   base,
		StubPath: stubPath,
		IDCount:  65536,
	})
	if err != nil {
		t.Logf("user namespaces unavailable: %v", err)
		return false
	}
	uns.Close()
	return true
}

func TestHelloEndToEnd(t *testing.T) {
	requireE2E(t)

	stagingDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stagingDir, "pkgs"), 0755); err != nil {
		t.Fatal(err)
	}
	store := sharedE2E.store
	stubPath := sharedE2E.stubPath
	ctx := log.WithTarget(context.Background(), log.Discard)

	// 1. Import: fetch upstream source, write pkgs/hello/{src/debian,sources.yml}.
	t.Run("import", func(t *testing.T) {
		res, err := importer.Import(importer.ImportConfig{
			Ctx:       ctx,
			Store:     store,
			OutputDir: stagingDir,
			NoVerify:  true,
		}, "hello")
		if err != nil {
			t.Fatalf("Import(hello): %v", err)
		}
		if res.Name != "hello" {
			t.Errorf("ImportResult.Name = %q, want hello", res.Name)
		}
		if len(res.Sources) == 0 {
			t.Errorf("expected at least one orig tarball in ImportResult.Sources")
		}
		// On-disk layout the next stages will read.
		controlPath := filepath.Join(stagingDir, "pkgs", "hello", "src", "debian", "control")
		if _, err := os.Stat(controlPath); err != nil {
			t.Errorf("expected %s to exist after import: %v", controlPath, err)
		}
		sourcesYML := filepath.Join(stagingDir, "pkgs", "hello", "sources.yml")
		if _, err := os.Stat(sourcesYML); err != nil {
			t.Errorf("expected sources.yml after import: %v", err)
		}
	})

	// 2. Lockfile: resolve hello's Build-Depends from the Debian testing index,
	// fetch every .deb, write build-deps.yml pointing at the index blob.
	t.Run("lockfile", func(t *testing.T) {
		res, err := lockfile.Generate(lockfile.Config{
			Ctx:       ctx,
			Store:     store,
			Arch:      "amd64",
			OutputDir: stagingDir,
			PkgName:   "hello",
		})
		if err != nil {
			t.Fatalf("lockfile.Generate(hello): %v", err)
		}
		if res.BlobHash.IsZero() {
			t.Error("expected non-zero lockfile blob hash")
		}
		if res.Packages == 0 {
			t.Error("expected non-zero number of resolved packages")
		}
		if !store.Blobs.Has(res.BlobHash) {
			t.Errorf("lockfile blob %s not in store", res.BlobHash)
		}
		// build-deps.yml must point at the freshly-stored lockfile.
		got, err := buildcfg.LoadArchHashYML(filepath.Join(stagingDir, "pkgs", "hello", "build-deps.yml"), "amd64")
		if err != nil {
			t.Fatalf("LoadArchHashYML: %v", err)
		}
		if got != res.BlobHash {
			t.Errorf("build-deps.yml has %s, expected %s", got, res.BlobHash)
		}
	})

	// 3. Source build: run dpkg-buildpackage inside a chroot. Requires user
	// namespaces; skip if unavailable. The output is a manifest blob listing
	// the produced .deb(s) and their control stanzas.
	if !hasUserNS(t, ctx, stubPath) {
		t.Skip("user namespaces required for source-build and binary-validate subtests")
	}

	// Insert lockfile_deps for hello → [libc6] BEFORE the binary validation
	// step. The locality gate would otherwise reject hello because libc6 is
	// not in the local build set. Done here (not in testdata) because hello's
	// build.yml comes from `gl import` and is freshly-generated each time;
	// editing it is exactly what a real user would do.
	if err := os.WriteFile(
		filepath.Join(stagingDir, "pkgs", "hello", "build.yml"),
		[]byte("lockfile_deps:\n  hello: [libc6]\n"),
		0644,
	); err != nil {
		t.Fatal(err)
	}

	ps, err := NewPackageSet(filepath.Join(stagingDir, "pkgs"), "amd64", store, stubPath)
	if err != nil {
		t.Fatalf("NewPackageSet: %v", err)
	}

	srcBuild := ps.sourceBuilds["hello"]
	if srcBuild == nil {
		t.Fatal("hello not in package set")
	}

	var srcOutputs []artifact.Output
	t.Run("source-build", func(t *testing.T) {
		outs, err := srcBuild.Build(artifact.BuildContext{
			Ctx:    ctx,
			Store:  store,
			Inputs: map[string]objstore.Hash{},
		})
		if err != nil {
			t.Fatalf("DebianPkgBuild.Build(hello): %v", err)
		}
		srcOutputs = outs

		var sawDeb, sawControl bool
		for _, o := range outs {
			if o.Name == "hello.deb" {
				sawDeb = true
			}
			if o.Name == "control:hello" {
				sawControl = true
			}
			if o.Hash.IsZero() {
				t.Errorf("output %q has zero hash", o.Name)
			}
		}
		if !sawDeb {
			t.Errorf("expected hello.deb in outputs, got %v", outputNames(outs))
		}
		if !sawControl {
			t.Errorf("expected control:hello in outputs, got %v", outputNames(outs))
		}
	})

	// 4. Binary validation: locality + installCheck. Inputs come from the
	// source build's outputs (the engine would normally splice these in).
	t.Run("binary-validate", func(t *testing.T) {
		if len(srcOutputs) == 0 {
			t.Skip("source build did not run; binary-validate has nothing to consume")
		}
		bp, err := ps.Binary("hello", "hello")
		if err != nil {
			t.Fatalf("PackageSet.Binary(hello, hello): %v", err)
		}
		inputs := map[string]objstore.Hash{}
		for _, o := range srcOutputs {
			inputs[o.Name] = o.Hash
		}
		if _, err := bp.Build(artifact.BuildContext{
			Ctx:    ctx,
			Store:  store,
			Inputs: inputs,
		}); err != nil {
			t.Fatalf("debianBinaryPkg.Build(hello): %v", err)
		}
	})
}

// outputNames is a small helper to render an []artifact.Output list as
// readable text in failure messages.
func outputNames(outs []artifact.Output) string {
	if len(outs) == 0 {
		return "[]"
	}
	names := make([]string, 0, len(outs))
	for _, o := range outs {
		names = append(names, o.Name)
	}
	return fmt.Sprintf("%v", names)
}
