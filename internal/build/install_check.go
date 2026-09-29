package build

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"gl-ng/internal/container"
	"gl-ng/internal/debian/deb822"
	"gl-ng/internal/debian/index"
	"gl-ng/internal/install"
	"gl-ng/internal/log"
	"gl-ng/internal/objstore"
)

// installCheck verifies that a binary package can be properly installed when
// all its runtime dependencies come from the local build set only.
//
// Approach:
//  1. Bootstrap container from lockfile essential packages (dpkg --unpack + --configure)
//  2. Wipe dpkg database — amnesia (files stay, dpkg forgets)
//  3. Resolve test package from local-only index
//  4. dpkg --install the complete resolved set
//
// If any dep is not locally built, resolution fails. If dpkg finds an issue,
// install fails. Both are correct signals that the build universe is incomplete.
func (b *debianBinaryPkg) installCheck(ctx context.Context, store *objstore.Store, debHash objstore.Hash, controlHash objstore.Hash) error {
	l := log.From(ctx, log.InstallCheck)

	// 1. Load lockfile index (for bootstrap only — not merged with local)
	lockfileHash, err := b.sourceBuild.loadLockfileHash()
	if err != nil {
		return fmt.Errorf("load lockfile for install check: %w", err)
	}
	lockfileReader, err := store.OpenBlob(lockfileHash)
	if err != nil {
		return fmt.Errorf("open lockfile blob: %w", err)
	}
	lockfileIndex, err := index.Load(lockfileReader)
	lockfileReader.Close()
	if err != nil {
		return fmt.Errorf("parse lockfile: %w", err)
	}

	// 2. Build local-only index (NO merge with lockfile)
	localIndex := b.buildLocalIndex(store)

	// Add the test package itself
	testPkg := b.buildTestPackageEntry(store, controlHash, debHash)
	if testPkg != nil {
		localIndex.Add(testPkg)
	}

	l.Info("%s: %d local packages for install check", b.name, localIndex.Len())

	// 2b. Gather lockfile_deps declared on this binary. They are strictly
	// local to the declaring binary — not inherited from extraDeps/includes —
	// so a consumer that needs libgcc-s1 (because its closure includes libc6)
	// must declare its own lockfile_deps entry for it.
	lockfileDepNames := b.lockfileDepNames()
	if len(lockfileDepNames) > 0 {
		lockfilePkgs, err := install.Resolve(lockfileIndex, b.sourceBuild.Arch, lockfileDepNames)
		if err != nil {
			return fmt.Errorf("resolve lockfile_deps for %s: %w", b.name, err)
		}
		for _, pkg := range lockfilePkgs {
			if localIndex.Get(pkg.Name) == nil {
				localIndex.Add(pkg)
			}
		}
		l.Info("%s: added %d lockfile_deps packages", b.name, len(lockfilePkgs))
	}

	// 3. Resolve test package from local-only index
	resolved, err := install.Resolve(localIndex, b.sourceBuild.Arch, []string{b.name})
	if err != nil {
		return fmt.Errorf("resolve %s from local index: %w", b.name, err)
	}
	l.Info("%s: resolved %d packages for install", b.name, len(resolved))

	// 4. Create namespace stack: BaseExecEnv → UserNS → MountNS
	stubPath := b.sourceBuild.StubPath
	if stubPath == "" {
		stubPath = container.StubPath()
	}

	stack, err := container.NewStack(container.StackConfig{
		Ctx:      ctx,
		StubPath: stubPath,
	})
	if err != nil {
		return err
	}
	mountNS := stack.MountNS

	nsCleanup := func() {
		stack.Close()
	}

	// Ensure /tmp exists for Bootstrap's Mktemp
	mountNS.Mkdir("/tmp", 01777)

	// 5. Bootstrap container from lockfile (essential packages)
	cont, rootfsPath, contCleanup, err := install.Bootstrap(ctx, mountNS, store, lockfileIndex, b.sourceBuild.Arch, stubPath)
	if err != nil {
		nsCleanup()
		return fmt.Errorf("bootstrap for install check: %w", err)
	}
	defer func() {
		if contCleanup != nil {
			contCleanup()
		}
		nsCleanup()
	}()

	// 6. Wipe dpkg status — dpkg amnesia
	statusPath := rootfsPath + "/var/lib/dpkg/status"
	f, err := mountNS.Open(statusPath, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return fmt.Errorf("wipe dpkg status: %w", err)
	}
	f.Close()

	// 7. Install resolved packages via dpkg --install
	if err := install.InstallResolved(ctx, cont, mountNS, store, rootfsPath, resolved); err != nil {
		return fmt.Errorf("install check failed for %s: %w", b.name, err)
	}

	l.Info("%s: install check passed", b.name)
	return nil
}

// buildTestPackageEntry constructs an index.Package for the package under test
// from its control blob and .deb hash.
func (b *debianBinaryPkg) buildTestPackageEntry(store *objstore.Store, controlHash objstore.Hash, debHash objstore.Hash) *index.Package {
	controlReader, err := store.OpenBlob(controlHash)
	if err != nil {
		return nil
	}
	d822Reader := deb822.NewReader(controlReader)
	stanza, err := d822Reader.Next()
	controlReader.Close()
	if err != nil {
		return nil
	}
	pkg, err := index.ParsePackageFromStanza(stanza)
	if err != nil {
		return nil
	}
	if !debHash.IsZero() {
		pkg.SHA256 = debHash.String()
		pkg.Stanza["sha256"] = debHash.String()
		if info, err := os.Stat(store.Blobs.Path(debHash)); err == nil {
			pkg.Size = info.Size()
			pkg.Stanza["size"] = strconv.FormatInt(info.Size(), 10)
		}
	}
	return pkg
}

// buildLocalIndex creates a package index from the source-build manifests
// reachable via extraDeps and includes from this binary. Source-build manifests
// are used directly (rather than per-binary validated manifests) so that sibling
// Includes — whose own validation may not yet have completed by the time this
// binary's Build() runs — are still discoverable: the parent source build is a
// Depends and therefore always available.
func (b *debianBinaryPkg) buildLocalIndex(store *objstore.Store) *index.Index {
	b.resolveExtraDeps()
	roots := append([]*debianBinaryPkg{}, b.extraDeps...)
	roots = append(roots, b.includes...)
	return makeLocalIndex(roots, store)
}
