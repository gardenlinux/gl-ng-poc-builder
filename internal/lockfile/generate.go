package lockfile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"gl-ng/internal/buildcfg"
	"gl-ng/internal/debian/aptrepo"
	"gl-ng/internal/debian/deb822"
	"gl-ng/internal/debian/depends"
	"gl-ng/internal/debian/index"
	"gl-ng/internal/log"
	"gl-ng/internal/objstore"
	"gl-ng/internal/resolver"
	"gl-ng/internal/stream"
)

type Config struct {
	Ctx       context.Context
	Store     *objstore.Store
	RepoURL   string
	Dist      string
	Arch      string
	OutputDir string
	PkgName   string
	Cookie    string
}

type Result struct {
	BlobHash objstore.Hash
	Arch     string
	Packages int
}

func Generate(cfg Config) (*Result, error) {
	cfg.applyDefaults()
	l := log.From(cfg.Ctx, log.Lockfile)

	pkgDir := filepath.Join(cfg.OutputDir, "pkgs", cfg.PkgName)
	controlPath := filepath.Join(pkgDir, "src", "debian", "control")
	buildDeps, err := extractBuildDeps(controlPath)
	if err != nil {
		return nil, fmt.Errorf("extract build-deps: %w", err)
	}

	activeProfiles := buildcfg.LoadBuildYML(pkgDir).BuildProfiles

	idx, err := FetchBinaryIndex(cfg.Ctx, cfg.Store, cfg.RepoURL, cfg.Dist, cfg.Arch, cfg.Cookie)
	if err != nil {
		return nil, fmt.Errorf("fetch binary index: %w", err)
	}

	roots := buildResolverRoots(buildDeps, idx, cfg.Arch, activeProfiles)

	blobHash, debHashes, n, err := runResolveFetchAndStore(cfg.Ctx, cfg.Store, l, idx, roots, cfg.Arch, cfg.RepoURL)
	if err != nil {
		return nil, err
	}

	depsPath := filepath.Join(cfg.OutputDir, "pkgs", cfg.PkgName, "build-deps.yml")
	if err := writeBuildDepsYML(depsPath, cfg.Arch, blobHash); err != nil {
		return nil, fmt.Errorf("write build-deps.yml: %w", err)
	}

	// Auto-pin: one per-arch build-deps pin covering the lockfile index blob
	// plus every .deb it references (oci-cache-design.md §6). Pin creation is
	// load-bearing — it must complete before we report success.
	releaseDate := fetchReleaseDate(cfg.Ctx, cfg.Store, cfg.RepoURL, cfg.Dist, cfg.Cookie)
	pinName := fmt.Sprintf("%s build-deps (%s, %s, %s from %s)",
		cfg.PkgName, cfg.Arch, cfg.Dist, releaseDate, cfg.RepoURL)
	if _, err := cfg.Store.Pins.Create(pinName, append([]objstore.Hash{blobHash}, debHashes...)); err != nil {
		return nil, fmt.Errorf("create build-deps pin: %w", err)
	}

	return &Result{
		BlobHash: blobHash,
		Arch:     cfg.Arch,
		Packages: n,
	}, nil
}

func (cfg *Config) applyDefaults() {
	if cfg.RepoURL == "" {
		cfg.RepoURL = "https://deb.debian.org/debian"
	}
	if cfg.Dist == "" {
		cfg.Dist = "testing"
	}
	if cfg.Arch == "" {
		cfg.Arch = detectArch()
	}
	if cfg.Ctx == nil {
		cfg.Ctx = context.Background()
	}
}

// runResolveFetchAndStore runs the resolver, fetches the resolved .deb blobs,
// serializes the resolved package set into a Packages-style index, and stores
// it as a single blob. Returns the index blob hash, the .deb blob hashes of
// every resolved package, and the number of packages. The .deb hashes are
// returned so the caller can build a build-deps pin (oci-cache-design.md §6)
// covering the index blob plus every .deb it references.
func runResolveFetchAndStore(ctx context.Context, store *objstore.Store, l *log.Logger,
	idx *index.Index, roots []resolver.Requirement, arch, repoURL string,
) (objstore.Hash, []objstore.Hash, int, error) {
	r := resolver.New(idx, arch)
	result, err := r.Resolve(roots)
	if err != nil {
		return objstore.Hash{}, nil, 0, fmt.Errorf("resolve dependencies: %w", err)
	}

	l.Info("resolved %d packages, fetching .deb blobs", len(result.Packages))
	if err := FetchDebs(ctx, store, repoURL, result.Packages); err != nil {
		return objstore.Hash{}, nil, 0, fmt.Errorf("fetch .deb blobs: %w", err)
	}

	debHashes := make([]objstore.Hash, 0, len(result.Packages))
	for _, pkg := range result.Packages {
		if pkg.SHA256 == "" {
			continue
		}
		h, err := objstore.NewHash(pkg.SHA256)
		if err != nil {
			return objstore.Hash{}, nil, 0, fmt.Errorf("package %s: invalid hash: %w", pkg.Name, err)
		}
		debHashes = append(debHashes, h)
	}

	indexBlob := strings.NewReader(serializeIndex(result.Packages))
	blobHash, err := store.Blobs.Store(indexBlob)
	if err != nil {
		return objstore.Hash{}, nil, 0, fmt.Errorf("store lockfile blob: %w", err)
	}
	return blobHash, debHashes, len(result.Packages), nil
}

// indexSkipFields are stanza keys that are stripped from the serialized
// lockfile index — they're either local to the upstream Packages file
// (filename, size) or already implicit elsewhere in the artifact graph.
var indexSkipFields = map[string]struct{}{
	"filename": {},
	"size":     {},
}

// serializeIndex turns a resolved package set into a deb822-style Packages
// index, dropping local-only fields and emitting stanza keys in
// alphabetically-stable order so the resulting blob hash is deterministic.
func serializeIndex(packages []*index.Package) string {
	var buf strings.Builder
	for _, pkg := range packages {
		keys := make([]string, 0, len(pkg.Stanza))
		for k := range pkg.Stanza {
			if _, skip := indexSkipFields[k]; skip {
				continue
			}
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			buf.WriteString(fmt.Sprintf("%s: %s\n", capitalizeField(k), pkg.Stanza[k]))
		}
		buf.WriteString("\n")
	}
	return buf.String()
}

func extractBuildDeps(controlPath string) (depends.DependencyList, error) {
	f, err := os.Open(controlPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	reader := deb822.NewReader(f)
	stanza, err := reader.Next()
	if err != nil {
		return nil, fmt.Errorf("read control stanza: %w", err)
	}

	var allDeps string
	if bd, ok := stanza["build-depends"]; ok {
		allDeps = bd
	}
	if bda, ok := stanza["build-depends-arch"]; ok {
		if allDeps != "" {
			allDeps += ", "
		}
		allDeps += bda
	}
	if bdi, ok := stanza["build-depends-indep"]; ok {
		if allDeps != "" {
			allDeps += ", "
		}
		allDeps += bdi
	}

	if allDeps == "" {
		return nil, nil
	}

	return depends.Parse(allDeps)
}

func FetchBinaryIndex(ctx context.Context, store *objstore.Store, repoURL, dist, arch, cookie string) (*index.Index, error) {
	l := log.From(ctx, log.Lockfile)

	releasePayload, err := aptrepo.FetchInRelease(ctx, aptrepo.FetchConfig{
		Store:     store,
		RepoURL:   repoURL,
		Dist:      dist,
		Cookie:    cookie,
		NoVerify:  true,
		Component: log.Lockfile,
	})
	if err != nil {
		return nil, fmt.Errorf("fetch InRelease: %w", err)
	}

	releaseHashes, err := aptrepo.ParseReleaseHashes(releasePayload)
	if err != nil {
		return nil, fmt.Errorf("parse Release hashes: %w", err)
	}

	packagesPath := fmt.Sprintf("main/binary-%s/Packages.gz", arch)
	expectedHash, ok := releaseHashes[packagesPath]
	if !ok {
		return nil, fmt.Errorf("%s not found in Release file", packagesPath)
	}

	// Check blob cache
	var packagesCompressed []byte
	packagesHash, hashErr := objstore.NewHash(expectedHash)
	if hashErr == nil && store.Blobs.Has(packagesHash) {
		l.Debug("Packages.gz cached (%s)", expectedHash[:12])
		rc, err := store.Blobs.Open(packagesHash)
		if err != nil {
			return nil, fmt.Errorf("read cached Packages.gz: %w", err)
		}
		packagesCompressed, err = io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read cached Packages.gz: %w", err)
		}
	} else {
		pkgURL := fmt.Sprintf("%s/dists/%s/%s", repoURL, dist, packagesPath)
		l.Info("downloading %s", pkgURL)
		resp, err := http.Get(pkgURL)
		if err != nil {
			return nil, fmt.Errorf("fetch Packages.gz: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("fetch Packages.gz: status %d", resp.StatusCode)
		}
		packagesCompressed, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("read Packages.gz: %w", err)
		}

		actualHash := sha256Hex(packagesCompressed)
		if actualHash != expectedHash {
			return nil, fmt.Errorf("Packages.gz hash mismatch: got %s, want %s", actualHash, expectedHash)
		}

		if _, err := store.Blobs.Store(bytes.NewReader(packagesCompressed)); err != nil {
			l.Warn("failed to cache Packages.gz: %v", err)
		}
	}

	decompressed, err := stream.GzipDecompress(bytes.NewReader(packagesCompressed))
	if err != nil {
		return nil, fmt.Errorf("decompress Packages.gz: %w", err)
	}
	defer decompressed.Close()

	return index.Load(decompressed)
}

func buildResolverRoots(buildDeps depends.DependencyList, idx *index.Index, arch string, activeProfiles []string) []resolver.Requirement {
	var roots []resolver.Requirement

	for _, pkg := range idx.EssentialPackages() {
		roots = append(roots, resolver.Requirement{
			Name: pkg.Name,
		})
	}

	implicitDeps := []string{"build-essential", "fakeroot", "debconf"}
	for _, name := range implicitDeps {
		roots = append(roots, resolver.Requirement{
			Name: name,
		})
	}

	for _, alt := range buildDeps {
		if len(alt) == 0 {
			continue
		}
		var matchedDep *depends.Dependency
		for i := range alt {
			if alt[i].MatchesArch(arch) && !alt[i].ExcludedByProfiles(activeProfiles) {
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

	return roots
}

func writeBuildDepsYML(path, arch string, hash objstore.Hash) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	content := fmt.Sprintf("%s: %s\n", arch, hash)
	return os.WriteFile(path, []byte(content), 0644)
}

// RootfsConfig holds parameters for generating a rootfs-level lockfile.
type RootfsConfig struct {
	Ctx       context.Context
	Store     *objstore.Store
	RepoURL   string
	Dist      string
	Arch      string
	OutputDir string
	Cookie    string
}

// GenerateRootfs generates a rootfs lockfile that captures the Debian packaging
// infrastructure needed for Layer 1 (dpkg, apt, perl, debconf, and all their
// transitive dependencies). These packages are used only during rootfs assembly
// to run postinst scripts — they are NOT included in the final rootfs output.
func GenerateRootfs(cfg RootfsConfig) (*Result, error) {
	cfg.applyDefaults()
	l := log.From(cfg.Ctx, log.Lockfile)

	idx, err := FetchBinaryIndex(cfg.Ctx, cfg.Store, cfg.RepoURL, cfg.Dist, cfg.Arch, cfg.Cookie)
	if err != nil {
		return nil, fmt.Errorf("fetch binary index: %w", err)
	}

	roots := buildRootfsRoots(idx)

	blobHash, debHashes, n, err := runResolveFetchAndStore(cfg.Ctx, cfg.Store, l, idx, roots, cfg.Arch, cfg.RepoURL)
	if err != nil {
		return nil, err
	}

	depsPath := filepath.Join(cfg.OutputDir, "rootfs-deps.yml")
	if err := writeBuildDepsYML(depsPath, cfg.Arch, blobHash); err != nil {
		return nil, fmt.Errorf("write rootfs-deps.yml: %w", err)
	}

	// Auto-pin: one per-arch rootfs build-deps pin (oci-cache-design.md §6).
	releaseDate := fetchReleaseDate(cfg.Ctx, cfg.Store, cfg.RepoURL, cfg.Dist, cfg.Cookie)
	pinName := fmt.Sprintf("rootfs build-deps (%s, %s, %s from %s)",
		cfg.Arch, cfg.Dist, releaseDate, cfg.RepoURL)
	if _, err := cfg.Store.Pins.Create(pinName, append([]objstore.Hash{blobHash}, debHashes...)); err != nil {
		return nil, fmt.Errorf("create rootfs build-deps pin: %w", err)
	}

	return &Result{
		BlobHash: blobHash,
		Arch:     cfg.Arch,
		Packages: n,
	}, nil
}

// fetchReleaseDate returns the InRelease Date field for pin labeling. It reuses
// the cookie-shared cached InRelease (cheap), and returns "unknown" on any
// failure so pin creation never hinges on date parsing.
func fetchReleaseDate(ctx context.Context, store *objstore.Store, repoURL, dist, cookie string) string {
	payload, err := aptrepo.FetchInRelease(ctx, aptrepo.FetchConfig{
		Store:     store,
		RepoURL:   repoURL,
		Dist:      dist,
		Cookie:    cookie,
		NoVerify:  true,
		Component: log.Lockfile,
	})
	if err != nil {
		return "unknown"
	}
	date, err := aptrepo.ParseReleaseDate(payload)
	if err != nil || date == "" {
		return "unknown"
	}
	return date
}

func (cfg *RootfsConfig) applyDefaults() {
	if cfg.RepoURL == "" {
		cfg.RepoURL = "https://deb.debian.org/debian"
	}
	if cfg.Dist == "" {
		cfg.Dist = "testing"
	}
	if cfg.Arch == "" {
		cfg.Arch = detectArch()
	}
	if cfg.Ctx == nil {
		cfg.Ctx = context.Background()
	}
}

// buildRootfsRoots constructs the resolver requirements for the rootfs Layer 1
// lockfile: every Essential: yes package plus the postinst-script
// infrastructure (perl-base for perl-driven scripts, mawk as the default
// awk). apt is intentionally excluded — rootfs assembly is dpkg-only.
func buildRootfsRoots(idx *index.Index) []resolver.Requirement {
	var roots []resolver.Requirement
	for _, pkg := range idx.EssentialPackages() {
		roots = append(roots, resolver.Requirement{Name: pkg.Name})
	}
	for _, name := range []string{"perl-base", "mawk"} {
		roots = append(roots, resolver.Requirement{Name: name})
	}
	return roots
}

func capitalizeField(s string) string {
	if s == "" {
		return s
	}
	parts := strings.Split(s, "-")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "-")
}

var (
	detectArchOnce sync.Once
	detectedArch   string
)

func detectArch() string {
	detectArchOnce.Do(func() {
		detectedArch = buildcfg.HostArch()
	})
	return detectedArch
}

// detectArchUncached is the legacy dpkg-only probe retained for tests that
// exercise the fallback behavior in isolation.
func detectArchUncached() string {
	out, err := exec.Command("dpkg", "--print-architecture").Output()
	if err != nil {
		return "amd64"
	}
	return strings.TrimSpace(string(out))
}

func FetchDebs(ctx context.Context, store *objstore.Store, repoURL string, packages []*index.Package) error {
	l := log.From(ctx, log.Fetch)
	var wg sync.WaitGroup
	errCh := make(chan error, len(packages))
	sem := make(chan struct{}, 16)

	var fetched atomic.Int32
	var cached atomic.Int32
	total := len(packages)

	for _, pkg := range packages {
		wg.Add(1)
		go func(p *index.Package) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if p.SHA256 == "" {
				return
			}
			h, err := objstore.NewHash(p.SHA256)
			if err != nil {
				errCh <- fmt.Errorf("%s: invalid hash: %w", p.Name, err)
				return
			}
			if store.Blobs.Has(h) {
				cached.Add(1)
				return
			}
			if p.Filename == "" {
				errCh <- fmt.Errorf("%s: no Filename field", p.Name)
				return
			}

			url := fmt.Sprintf("%s/%s", repoURL, p.Filename)
			resp, err := http.Get(url)
			if err != nil {
				errCh <- fmt.Errorf("fetch %s: %w", p.Name, err)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				errCh <- fmt.Errorf("fetch %s: HTTP %d", p.Name, resp.StatusCode)
				return
			}

			data, err := io.ReadAll(resp.Body)
			if err != nil {
				errCh <- fmt.Errorf("read %s: %w", p.Name, err)
				return
			}

			actualHash := sha256.Sum256(data)
			actualHex := hex.EncodeToString(actualHash[:])
			if actualHex != p.SHA256 {
				errCh <- fmt.Errorf("hash mismatch for %s: got %s, want %s", p.Name, actualHex, p.SHA256)
				return
			}

			if _, err := store.Blobs.Store(bytes.NewReader(data)); err != nil {
				errCh <- fmt.Errorf("store %s: %w", p.Name, err)
				return
			}

			n := fetched.Add(1)
			if n%10 == 0 || int(n)+int(cached.Load()) == total {
				l.Info("%d/%d fetched, %d cached", n, total, cached.Load())
			}
		}(pkg)
	}

	wg.Wait()
	close(errCh)

	l.Info("fetch complete: %d fetched, %d cached", fetched.Load(), cached.Load())

	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return fmt.Errorf("%d fetch errors (first: %w)", len(errs), errs[0])
	}
	return nil
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
