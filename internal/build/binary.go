package build

import (
	"fmt"
	"strings"

	"gl-ng/internal/artifact"
	"gl-ng/internal/buildcfg"
	"gl-ng/internal/debian/deb822"
	"gl-ng/internal/debian/depends"
	"gl-ng/internal/log"
	"gl-ng/internal/objstore"
)

// debianBinaryPkg is a validation gate artifact. It does NOT perform compilation.
// It selects a specific named binary package from its parent source build's
// outputs, verifies it was actually produced, and checks that all runtime
// dependencies (Depends, Pre-Depends) are satisfiable from the local build set.
// This ensures that every package in the final rootfs was built from source by us.
// Created exclusively via DebianPkgBuild.Binary(name).
type debianBinaryPkg struct {
	name         string
	sourceBuild  *DebianPkgBuild
	extraDeps    []*debianBinaryPkg
	includes     []*debianBinaryPkg
	store        *objstore.Store
	identity     objstore.Hash
	pkgSet       *PackageSet
	depsResolved bool
}

func (b *debianBinaryPkg) Key() string {
	return fmt.Sprintf("debian-binary-pkg:%s:%s:%s", b.sourceBuild.Name, b.name, b.sourceBuild.Arch)
}

func (b *debianBinaryPkg) String() string {
	return fmt.Sprintf("binary-pkg:%s", b.name)
}

func (b *debianBinaryPkg) Depends() []artifact.Artifact {
	b.resolveExtraDeps()
	deps := []artifact.Artifact{b.sourceBuild}
	for _, extra := range b.extraDeps {
		deps = append(deps, extra)
	}
	return deps
}

// Includes returns sibling binary packages from the same source build that
// this binary references via runtime_depends. They contribute to closure but
// not to build order — sibling binaries are co-produced by the parent source
// build (a Depends), so explicit ordering would only create artificial cycles
// where Debian's control file already declares mutual Depends (e.g.,
// libssl3t64 ↔ openssl-provider-legacy).
func (b *debianBinaryPkg) Includes() []artifact.Artifact {
	b.resolveExtraDeps()
	out := make([]artifact.Artifact, 0, len(b.includes))
	for _, inc := range b.includes {
		out = append(out, inc)
	}
	return out
}

func (b *debianBinaryPkg) resolveExtraDeps() {
	if b.depsResolved || b.pkgSet == nil {
		return
	}
	b.depsResolved = true

	buildYML := buildcfg.LoadBuildYML(b.sourceBuild.PkgDir)

	addDep := func(srcName, binName string) {
		bp, err := b.pkgSet.Binary(srcName, binName)
		if err != nil {
			return
		}
		if bp.sourceBuild == b.sourceBuild {
			b.includes = append(b.includes, bp)
		} else {
			b.extraDeps = append(b.extraDeps, bp)
		}
	}

	// Per-binary runtime_depends from build.yml
	if rdeps, ok := buildYML.RuntimeDepends[b.name]; ok {
		for _, depSpec := range rdeps {
			parts := strings.SplitN(depSpec, ":", 2)
			if len(parts) != 2 {
				continue
			}
			addDep(parts[0], parts[1])
		}
	}

	// depends entries from build.yml also add to locality (they're locally-built)
	for _, depSpec := range buildYML.Depends {
		parts := strings.SplitN(depSpec, ":", 2)
		if len(parts) != 2 {
			continue
		}
		addDep(parts[0], parts[1])
	}
}

// Inputs references the .deb file and control metadata from the source build,
// plus the same artefacts for any sibling Includes (whose validation is
// concurrent, so we sourced their .deb directly from the parent source build).
// The engine resolves these to actual blob hashes before calling Build().
func (b *debianBinaryPkg) Inputs() []artifact.Input {
	b.resolveExtraDeps()
	inputs := []artifact.Input{
		{Source: b.sourceBuild, Name: b.name + ".deb"},
		{Source: b.sourceBuild, Name: "control:" + b.name},
	}
	for _, sib := range b.includes {
		inputs = append(inputs,
			artifact.Input{Source: b.sourceBuild, Name: sib.name + ".deb"},
			artifact.Input{Source: b.sourceBuild, Name: "control:" + sib.name},
		)
	}
	return inputs
}

func (b *debianBinaryPkg) OutputRefs(store *objstore.Store) (objstore.Hash, []objstore.Hash, error) {
	return artifact.ResolveOutputRefs(b, store)
}

func (b *debianBinaryPkg) Identity() (objstore.Hash, error) {
	if !b.identity.IsZero() {
		return b.identity, nil
	}

	b.resolveExtraDeps()

	parts := []string{"binary-pkg", b.name}

	srcID, err := b.sourceBuild.Identity()
	if err != nil {
		return objstore.Hash{}, err
	}
	parts = append(parts, srcID.String())

	for _, dep := range b.extraDeps {
		depID, err := dep.Identity()
		if err != nil {
			return objstore.Hash{}, fmt.Errorf("get dep identity for %s: %w", dep.name, err)
		}
		parts = append(parts, depID.String())
	}

	// Sibling includes: contribute Key() rather than Identity() to avoid mutual
	// recursion (sibling B's identity may include A and vice-versa). Same-source
	// content changes are already reflected via srcID, so Key is sufficient to
	// pin which siblings this validation gate references.
	for _, sib := range b.includes {
		parts = append(parts, "include:"+sib.Key())
	}

	hash := objstore.ConcatHash(parts...)
	b.identity = hash
	return hash, nil
}

// Build validates the binary package:
//  1. Verifies the .deb was actually produced by the source build
//  2. Parses control metadata (Depends, Pre-Depends)
//  3. Checks that ALL runtime dependencies are satisfiable from the local build
//     set — i.e., every dep must be a package we also build from source.
//     This is the locality constraint that guarantees the rootfs contains zero
//     mirrored binaries.
func (b *debianBinaryPkg) Build(ctx artifact.BuildContext) ([]artifact.Output, error) {
	l := log.From(ctx.Ctx, log.Binary)
	l.Info("validating %s", b.name)

	// Find the .deb hash from resolved inputs
	debHash, err := b.findDebHash(ctx.Inputs)
	if err != nil {
		return nil, fmt.Errorf("binary package %s: %w", b.name, err)
	}

	// Find the control hash from resolved inputs
	controlKey := "control:" + b.name
	controlHash, ok := ctx.Inputs[controlKey]
	if !ok {
		return nil, fmt.Errorf("binary package %s: control metadata not found", b.name)
	}

	// Validate locality: all runtime deps must come from local build set
	if err := b.validateLocality(ctx.Store, controlHash); err != nil {
		return nil, err
	}

	l.Info("%s: locality check passed", b.name)

	if err := b.installCheck(ctx.Ctx, ctx.Store, debHash, controlHash); err != nil {
		return nil, err
	}

	// Pass through as outputs — downstream consumers (rootfs) reference these
	return []artifact.Output{
		{Name: b.name + ".deb", Hash: debHash},
		{Name: controlKey, Hash: controlHash},
	}, nil
}

// findDebHash finds the .deb blob hash from resolved inputs.
func (b *debianBinaryPkg) findDebHash(inputs map[string]objstore.Hash) (objstore.Hash, error) {
	if h, ok := inputs[b.name+".deb"]; ok {
		return h, nil
	}
	return objstore.Hash{}, fmt.Errorf("no .deb found for %s in resolved inputs", b.name)
}

// validateLocality reads the control stanza, extracts Depends and Pre-Depends,
// and verifies every dependency is satisfiable from the local build set.
// This is a HARD check — any missing dep means the binary package cannot be
// included in the rootfs (since we'd need a mirrored binary to satisfy it).
func (b *debianBinaryPkg) validateLocality(store *objstore.Store, controlHash objstore.Hash) error {
	reader, err := store.OpenBlob(controlHash)
	if err != nil {
		return fmt.Errorf("binary package %s: open control blob: %w", b.name, err)
	}
	defer reader.Close()

	d822Reader := deb822.NewReader(reader)
	control, err := d822Reader.Next()
	if err != nil {
		return fmt.Errorf("binary package %s: parse control: %w", b.name, err)
	}

	var allDeps string
	if d, ok := control["depends"]; ok {
		allDeps = d
	}
	if pd, ok := control["pre-depends"]; ok {
		if allDeps != "" {
			allDeps += ", "
		}
		allDeps += pd
	}

	if allDeps == "" {
		return nil
	}

	depList, err := depends.Parse(allDeps)
	if err != nil {
		return fmt.Errorf("binary package %s: parse depends %q: %w", b.name, allDeps, err)
	}

	localSet := b.buildLocalSet()

	// lockfile_deps: packages explicitly declared as coming from the lockfile
	// for this binary's validation — skip these in locality enforcement
	lockfileAllowed := make(map[string]bool)
	buildYML := buildcfg.LoadBuildYML(b.sourceBuild.PkgDir)
	if ldeps, ok := buildYML.LockfileDeps[b.name]; ok {
		for _, name := range ldeps {
			lockfileAllowed[name] = true
		}
	}

	var unsatisfied []string
	for _, alt := range depList {
		if b.altSatisfiedLocally(alt, localSet) {
			continue
		}
		if b.altInLockfileDeps(alt, lockfileAllowed) {
			continue
		}
		var altNames []string
		for _, d := range alt {
			altNames = append(altNames, d.Name)
		}
		unsatisfied = append(unsatisfied, strings.Join(altNames, " | "))
	}

	if len(unsatisfied) > 0 {
		return fmt.Errorf("binary package %s: locality check failed — runtime deps not in local build set: [%s]",
			b.name, strings.Join(unsatisfied, ", "))
	}

	return nil
}

// buildLocalSet computes the transitive closure of this package's extraDeps
// and includes, returning all package names (and their virtual provides) that
// are reachable from this binary package via explicit runtime_depends, depends,
// and sibling Includes declarations.
func (b *debianBinaryPkg) buildLocalSet() map[string]bool {
	local := make(map[string]bool)
	var walk func(bp *debianBinaryPkg)
	walk = func(bp *debianBinaryPkg) {
		if local[bp.name] {
			return
		}
		local[bp.name] = true
		bp.resolveExtraDeps()
		for _, dep := range bp.extraDeps {
			walk(dep)
		}
		for _, inc := range bp.includes {
			walk(inc)
		}
	}
	walk(b)

	// Also add virtual packages (Provides) from all packages in this closure
	if b.pkgSet != nil {
		provides := b.pkgSet.ProvidesMap()
		for name := range local {
			for _, virt := range provides[name] {
				local[virt] = true
			}
		}
	}
	return local
}

// altSatisfiedLocally checks if at least one alternative in the dep is in the
// local build set.
func (b *debianBinaryPkg) altSatisfiedLocally(alt []depends.Dependency, localSet map[string]bool) bool {
	for _, dep := range alt {
		if localSet[dep.Name] {
			return true
		}
	}
	return false
}

// altInLockfileDeps checks if at least one alternative is explicitly declared
// as a lockfile dep for this binary package.
func (b *debianBinaryPkg) altInLockfileDeps(alt []depends.Dependency, allowed map[string]bool) bool {
	for _, dep := range alt {
		if allowed[dep.Name] {
			return true
		}
	}
	return false
}

// lockfileDepNames returns the lockfile_deps declared on this binary itself.
// Lockfile_deps are strictly local to the declaring binary: they are NOT
// inherited by consumers whose install closure happens to include this binary.
// When a consumer's install-check needs a name that this binary's Depends pull
// in transitively (e.g. libgcc-s1 from libc6), the consumer must declare its
// own lockfile_deps entry for that name. This keeps each binary's install
// closure resolved against ITS OWN per-source lockfile, which avoids version
// skew between independently SAT-resolved lockfiles (a real failure mode
// during gcc-N transitions).
func (b *debianBinaryPkg) lockfileDepNames() []string {
	buildYML := buildcfg.LoadBuildYML(b.sourceBuild.PkgDir)
	ldeps, ok := buildYML.LockfileDeps[b.name]
	if !ok {
		return nil
	}
	out := make([]string, len(ldeps))
	copy(out, ldeps)
	return out
}
