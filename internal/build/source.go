package build

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gl-ng/internal/artifact"
	"gl-ng/internal/buildcfg"
	"gl-ng/internal/container"
	"gl-ng/internal/debian/depends"
	"gl-ng/internal/debian/index"
	"gl-ng/internal/dirhash"
	"gl-ng/internal/log"
	"gl-ng/internal/objstore"
	"gl-ng/internal/resolver"
	"gl-ng/internal/stream"
)

// LoadBinaryPkg returns the index.Package for the named binary, read from the
// source build's manifest in the store. Returns nil if the source build
// hasn't been built yet (no manifest in the map) or the binary isn't in the
// manifest. This is the right path when a binary's own validation may not yet
// have completed (e.g. sibling Includes, which have no build-order
// relationship with the consumer). The source build IS always a Depends of
// any binary, so its manifest is always present.
func (s *DebianPkgBuild) LoadBinaryPkg(binaryName string, store *objstore.Store) *index.Package {
	srcID, err := s.Identity()
	if err != nil {
		return nil
	}
	manifestHash, err := store.MapGet(srcID)
	if err != nil {
		return nil
	}
	return parsePackageFromManifest(store, manifestHash, binaryName)
}

type DebianPkgBuild struct {
	Name         string
	PkgDir       string
	Arch         string
	DepArtifacts []artifact.Artifact
	StubPath     string
	store        *objstore.Store
	identity     objstore.Hash
	binaries     map[string]*debianBinaryPkg
	pkgSet       *PackageSet
	depsResolved bool
}

type DebianPkgBuildConfig struct {
	Name         string
	PkgDir       string
	Arch         string
	DepArtifacts []artifact.Artifact
	Store        *objstore.Store
	StubPath     string
}

func NewDebianPkgBuild(cfg DebianPkgBuildConfig) *DebianPkgBuild {
	pkgDir := cfg.PkgDir
	if abs, err := filepath.Abs(pkgDir); err == nil {
		pkgDir = abs
	}
	stubPath := cfg.StubPath
	if stubPath != "" {
		if abs, err := filepath.Abs(stubPath); err == nil {
			stubPath = abs
		}
	}
	return &DebianPkgBuild{
		Name:         cfg.Name,
		PkgDir:       pkgDir,
		Arch:         cfg.Arch,
		DepArtifacts: cfg.DepArtifacts,
		StubPath:     stubPath,
		store:        cfg.Store,
		binaries:     make(map[string]*debianBinaryPkg),
	}
}

// Binary returns (or creates) the debianBinaryPkg instance for the named binary
// package produced by this source build. Callers use this to construct the graph:
//
//	glibc := NewDebianPkgBuild(...)
//	libc6 := glibc.Binary("libc6")
func (d *DebianPkgBuild) Binary(name string) *debianBinaryPkg {
	if bp, ok := d.binaries[name]; ok {
		return bp
	}
	bp := &debianBinaryPkg{
		name:        name,
		sourceBuild: d,
		store:       d.store,
	}
	d.binaries[name] = bp
	return bp
}

func (s *DebianPkgBuild) Key() string {
	return fmt.Sprintf("debian-pkg-build:%s:%s", s.Name, s.Arch)
}

func (s *DebianPkgBuild) String() string {
	return fmt.Sprintf("debian-pkg-build:%s", s.Name)
}

func (s *DebianPkgBuild) Depends() []artifact.Artifact {
	s.resolveDeps()
	return s.DepArtifacts
}

// Includes returns no closure-only edges — source builds don't have any.
func (s *DebianPkgBuild) Includes() []artifact.Artifact { return nil }

func (s *DebianPkgBuild) OutputRefs(store *objstore.Store) (objstore.Hash, []objstore.Hash, error) {
	return artifact.ResolveOutputRefs(s, store)
}

func (s *DebianPkgBuild) resolveDeps() {
	if s.depsResolved || s.pkgSet == nil {
		return
	}
	s.depsResolved = true

	buildYML := buildcfg.LoadBuildYML(s.PkgDir)
	for _, depSpec := range buildYML.Depends {
		parts := strings.SplitN(depSpec, ":", 2)
		if len(parts) != 2 {
			continue
		}
		bp, err := s.pkgSet.Binary(parts[0], parts[1])
		if err != nil {
			continue
		}
		s.DepArtifacts = append(s.DepArtifacts, bp)
	}
}

func (s *DebianPkgBuild) Inputs() []artifact.Input {
	var inputs []artifact.Input
	for _, dep := range s.DepArtifacts {
		bp, ok := dep.(*debianBinaryPkg)
		if !ok {
			continue
		}
		inputs = append(inputs, artifact.Input{Source: bp, Name: bp.name + ".deb"})
		inputs = append(inputs, artifact.Input{Source: bp, Name: "control:" + bp.name})
	}
	return inputs
}

func (s *DebianPkgBuild) Identity() (objstore.Hash, error) {
	if !s.identity.IsZero() {
		return s.identity, nil
	}

	dirHash, err := dirhash.HashDirectory(s.PkgDir)
	if err != nil {
		return objstore.Hash{}, fmt.Errorf("hash package directory: %w", err)
	}

	parts := []string{dirHash, s.Arch}
	for _, dep := range s.DepArtifacts {
		id, err := dep.Identity()
		if err != nil {
			return objstore.Hash{}, fmt.Errorf("get dep identity: %w", err)
		}
		parts = append(parts, id.String())
	}

	hash := objstore.ConcatHash(parts...)
	s.identity = hash
	return hash, nil
}

func (s *DebianPkgBuild) Build(ctx artifact.BuildContext) ([]artifact.Output, error) {
	store := ctx.Store
	l := log.From(ctx.Ctx, log.Build)
	l.Info("building source package: %s", s.Name)

	l.Debug("loading lockfile")
	lockfileHash, err := s.loadLockfileHash()
	if err != nil {
		return nil, fmt.Errorf("load lockfile: %w", err)
	}

	lockfileReader, err := store.OpenBlob(lockfileHash)
	if err != nil {
		return nil, fmt.Errorf("open lockfile blob: %w", err)
	}
	defer lockfileReader.Close()

	lockfileIndex, err := index.Load(lockfileReader)
	if err != nil {
		return nil, fmt.Errorf("parse lockfile index: %w", err)
	}

	localIndex := s.buildLocalIndexForBuild(store)
	mergedIndex := lockfileIndex.Merge(localIndex)

	controlPath := filepath.Join(s.PkgDir, "src", "debian", "control")
	buildDeps, err := extractBuildDepsFromControl(controlPath)
	if err != nil {
		return nil, fmt.Errorf("parse build-depends: %w", err)
	}

	var roots []resolver.Requirement
	for _, pkg := range mergedIndex.EssentialPackages() {
		roots = append(roots, resolver.Requirement{Name: pkg.Name})
	}
	for _, tool := range []string{"build-essential", "fakeroot", "debconf"} {
		roots = append(roots, resolver.Requirement{Name: tool})
	}

	buildCfg := buildcfg.LoadBuildYML(s.PkgDir)
	for _, alt := range buildDeps {
		if len(alt) == 0 {
			continue
		}
		var matchedDep *depends.Dependency
		for i := range alt {
			if alt[i].MatchesArch(s.Arch) && !alt[i].ExcludedByProfiles(buildCfg.BuildProfiles) {
				matchedDep = &alt[i]
				break
			}
		}
		if matchedDep == nil {
			continue
		}
		req := resolver.Requirement{
			Name:            matchedDep.Name,
			VirtualEligible: true,
		}
		if matchedDep.Version != nil {
			req.VersionOp = matchedDep.Version.Op
			req.Version = matchedDep.Version.Version
		}
		roots = append(roots, req)
	}

	r := resolver.New(mergedIndex, s.Arch)
	resolved, err := r.Resolve(roots)
	if err != nil {
		return nil, fmt.Errorf("resolve build-depends: %w", err)
	}
	l.Info("resolved %d packages for build chroot", len(resolved.Packages))

	refs, _ := buildcfg.LoadSourcesYML(s.PkgDir)

	cont, cleanup, err := s.setupBuildEnv(ctx.Ctx, store, resolved, refs)
	if err != nil {
		return nil, fmt.Errorf("setup build env: %w", err)
	}
	defer cleanup()

	if err := s.runBuildInContainer(ctx.Ctx, cont, resolved.Packages); err != nil {
		return nil, fmt.Errorf("run build: %w", err)
	}

	outputs, err := s.collectOutputs(ctx.Ctx, cont, store)
	if err != nil {
		return nil, fmt.Errorf("collect outputs: %w", err)
	}

	return outputs, nil
}

func (s *DebianPkgBuild) loadLockfileHash() (objstore.Hash, error) {
	return buildcfg.LoadArchHashYML(filepath.Join(s.PkgDir, "build-deps.yml"), s.Arch)
}

func (s *DebianPkgBuild) computeVersion() (string, error) {
	_, baseVersion, _, err := s.parseChangelogTopEntry()
	if err != nil {
		return "0.0-0+gl~00000000", nil
	}

	srcDir := filepath.Join(s.PkgDir, "src")
	srcHash, err := dirhash.HashDirectory(srcDir)
	if err != nil {
		srcHash = "0000000000000000000000000000000000000000000000000000000000000000"
	}

	return baseVersion + "+gl~" + srcHash[:8], nil
}

// parseChangelogTopEntry reads the first entry of debian/changelog and
// returns the source name, the base version inside the parens, and the
// RFC2822-style date string from the entry's trailer line
// (" -- Name <email>  <date>"). The trailer date is what dpkg-buildpackage
// uses for SOURCE_DATE_EPOCH and for the .deb's metadata, so we mirror it
// onto the prepended gl entry to keep the produced .deb reproducible.
func (s *DebianPkgBuild) parseChangelogTopEntry() (name, baseVersion, date string, err error) {
	changelogPath := filepath.Join(s.PkgDir, "src", "debian", "changelog")
	data, err := os.ReadFile(changelogPath)
	if err != nil {
		return "", "", "", err
	}
	return parseChangelogTopEntryBytes(data)
}

func parseChangelogTopEntryBytes(data []byte) (name, baseVersion, date string, err error) {
	s := string(data)
	header, rest, _ := strings.Cut(s, "\n")

	open := strings.Index(header, "(")
	close := strings.Index(header, ")")
	if open < 0 || close < 0 || close <= open {
		return "", "", "", fmt.Errorf("malformed changelog header: %q", header)
	}
	name = strings.TrimSpace(header[:open])
	if name == "" {
		return "", "", "", fmt.Errorf("empty source name in changelog header: %q", header)
	}
	baseVersion = header[open+1 : close]

	for _, line := range strings.Split(rest, "\n") {
		if !strings.HasPrefix(line, " -- ") {
			continue
		}
		idx := strings.LastIndex(line, "  ")
		if idx < 0 {
			return "", "", "", fmt.Errorf("malformed changelog trailer (no date separator): %q", line)
		}
		date = strings.TrimSpace(line[idx+2:])
		if date == "" {
			return "", "", "", fmt.Errorf("empty date in changelog trailer: %q", line)
		}
		return name, baseVersion, date, nil
	}
	return "", "", "", fmt.Errorf("no trailer found in changelog top entry")
}

// formatGlChangelogEntry builds a debian/changelog entry that stamps the
// synthetic gl~ version onto the package. Authored by `nobody`, with the
// date copied from the existing top entry so dpkg-buildpackage propagates
// the same SOURCE_DATE_EPOCH it would have used otherwise.
func formatGlChangelogEntry(srcName, glVersion, date string) string {
	return srcName + " (" + glVersion + ") UNRELEASED; urgency=medium\n" +
		"\n" +
		"  * Build with gl-ng.\n" +
		"\n" +
		" -- nobody <nobody@localhost>  " + date + "\n" +
		"\n"
}

// stampGlChangelog prepends a synthetic changelog entry — stamped with the
// computed gl~ version and authored by `nobody` — to the source's
// debian/changelog inside the build's mount namespace. The existing
// changelog history is left untouched. The new entry's trailer date
// mirrors the existing top entry's so dpkg-buildpackage gets the same
// SOURCE_DATE_EPOCH it would otherwise have read.
//
// srcDst is the rootfs path of the source directory inside the mount NS
// (i.e., <rootfs>/src/<name>).
func (s *DebianPkgBuild) stampGlChangelog(mountNS *container.MountNS, srcDst string) error {
	srcName, _, date, err := s.parseChangelogTopEntry()
	if err != nil {
		return fmt.Errorf("parse changelog top entry: %w", err)
	}
	glVersion, err := s.computeVersion()
	if err != nil {
		return fmt.Errorf("compute gl version: %w", err)
	}
	hostChangelog, err := os.ReadFile(filepath.Join(s.PkgDir, "src", "debian", "changelog"))
	if err != nil {
		return fmt.Errorf("read changelog: %w", err)
	}

	newContent := append([]byte(formatGlChangelogEntry(srcName, glVersion, date)), hostChangelog...)
	f, err := mountNS.Open(srcDst+"/debian/changelog", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("open changelog for write: %w", err)
	}
	_, err = f.Write(newContent)
	f.Close()
	return err
}

func PackAsRootfsTar(dir string, store *objstore.Store) (objstore.Hash, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)

	if err := stream.TarCreate(dir, gw); err != nil {
		gw.Close()
		return objstore.Hash{}, fmt.Errorf("create tar: %w", err)
	}

	if err := gw.Close(); err != nil {
		return objstore.Hash{}, fmt.Errorf("close gzip: %w", err)
	}

	hash, err := store.Blobs.Store(bytes.NewReader(buf.Bytes()))
	if err != nil {
		return objstore.Hash{}, fmt.Errorf("store tar: %w", err)
	}

	return hash, nil
}
