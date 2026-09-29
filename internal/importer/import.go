// Package importer implements the source import tool for the gl-ng build system.
// It fetches Debian source packages from an APT repository, verifies their
// integrity, stores orig tarballs in the object store, and extracts the debian/
// packaging directory into the monorepo layout.
package importer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"gl-ng/internal/debian/aptrepo"
	"gl-ng/internal/debian/deb822"
	"gl-ng/internal/debian/version"
	"gl-ng/internal/log"
	"gl-ng/internal/objstore"
	"gl-ng/internal/stream"
)

// ImportConfig holds configuration for a source package import operation.
type ImportConfig struct {
	Ctx       context.Context
	Store     *objstore.Store
	RepoURL   string
	Dist      string
	Keyring   string
	OutputDir string

	NoVerify   bool
	Cookie     string
	HTTPClient *http.Client
}

// ImportResult contains the results of a successful source package import.
type ImportResult struct {
	Name    string
	Version string
	Format  string
	Sources []SourceEntry // orig tarballs stored
}

// SourceEntry represents a single source file stored in the object store.
type SourceEntry struct {
	Name string
	Hash objstore.Hash
}

// defaults fills in default values for unset configuration fields.
func (cfg *ImportConfig) defaults() {
	if cfg.RepoURL == "" {
		cfg.RepoURL = "https://deb.debian.org/debian"
	}
	if cfg.Dist == "" {
		cfg.Dist = "testing"
	}
	if cfg.Keyring == "" {
		cfg.Keyring = "/usr/share/keyrings/debian-archive-keyring.gpg"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
}

// Import fetches and imports a Debian source package from an APT repository.
// It downloads the InRelease file, verifies its GPG signature, finds the
// requested package in the Sources index, downloads all source files, and
// extracts the debian/ directory into the output directory.
func Import(cfg ImportConfig, packageName string) (*ImportResult, error) {
	cfg.defaults()

	if cfg.Store == nil {
		return nil, fmt.Errorf("importer: Store is required")
	}
	if cfg.OutputDir == "" {
		return nil, fmt.Errorf("importer: OutputDir is required")
	}
	if packageName == "" {
		return nil, fmt.Errorf("importer: package name is required")
	}

	releasePayload, err := aptrepo.FetchInRelease(cfg.Ctx, aptrepo.FetchConfig{
		Store:      cfg.Store,
		RepoURL:    cfg.RepoURL,
		Dist:       cfg.Dist,
		Cookie:     cfg.Cookie,
		Keyring:    cfg.Keyring,
		NoVerify:   cfg.NoVerify,
		HTTPClient: cfg.HTTPClient,
		Component:  log.Importer,
	})
	if err != nil {
		return nil, fmt.Errorf("importer: %w", err)
	}

	releaseHashes, err := aptrepo.ParseReleaseHashes(releasePayload)
	if err != nil {
		return nil, fmt.Errorf("importer: parsing Release hashes: %w", err)
	}

	releaseDate, err := aptrepo.ParseReleaseDate(releasePayload)
	if err != nil {
		return nil, fmt.Errorf("importer: parsing Release date: %w", err)
	}

	srcPkg, err := fetchSourcePackageStanza(cfg, releaseHashes, packageName)
	if err != nil {
		return nil, err
	}

	sourceFiles, err := downloadSourceFiles(cfg, srcPkg)
	if err != nil {
		return nil, fmt.Errorf("importer: downloading source files: %w", err)
	}

	result, err := extractDebianDir(cfg, srcPkg, sourceFiles)
	if err != nil {
		return nil, err
	}

	// Auto-pin the orig tarballs as a single source pin. Pin creation is
	// load-bearing (oci-cache-design.md §6): it must complete before we report
	// success, otherwise a GC between import and first remote push would delete
	// unrecoverable local-only work. A native package with no orig tarballs
	// produces no pin.
	if len(result.Sources) > 0 {
		blobs := make([]objstore.Hash, 0, len(result.Sources))
		for _, s := range result.Sources {
			blobs = append(blobs, s.Hash)
		}
		name := fmt.Sprintf("%s %s orig (%s, %s from %s)",
			srcPkg.Name, srcPkg.Version, cfg.Dist, releaseDate, cfg.RepoURL)
		if _, err := cfg.Store.Pins.Create(name, blobs); err != nil {
			return nil, fmt.Errorf("importer: creating source pin: %w", err)
		}
	}

	return result, nil
}

// fetchSourcePackageStanza downloads main/source/Sources.gz (using the SHA256
// from the verified Release hashes as a content-addressed cache key),
// decompresses it, and returns the highest-version stanza for packageName.
func fetchSourcePackageStanza(cfg ImportConfig, releaseHashes map[string]string, packageName string) (*sourcePackage, error) {
	l := log.From(cfg.Ctx, log.Importer)

	sourcesPath := "main/source/Sources.gz"
	expectedHash, ok := releaseHashes[sourcesPath]
	if !ok {
		return nil, fmt.Errorf("importer: %s not found in Release file", sourcesPath)
	}

	var sourcesCompressed []byte
	sourcesHash, hashErr := objstore.NewHash(expectedHash)
	if hashErr == nil && cfg.Store.Blobs.Has(sourcesHash) {
		l.Debug("Sources.gz cached (%s)", expectedHash[:12])
		rc, err := cfg.Store.Blobs.Open(sourcesHash)
		if err != nil {
			return nil, fmt.Errorf("importer: reading cached Sources.gz: %w", err)
		}
		sourcesCompressed, err = io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("importer: reading cached Sources.gz: %w", err)
		}
	} else {
		sourcesURL := fmt.Sprintf("%s/dists/%s/%s", cfg.RepoURL, cfg.Dist, sourcesPath)
		l.Info("downloading %s", sourcesURL)
		var err error
		sourcesCompressed, err = httpGet(cfg.HTTPClient, sourcesURL)
		if err != nil {
			return nil, fmt.Errorf("importer: fetching Sources.gz: %w", err)
		}

		actualHash := sha256Hex(sourcesCompressed)
		if actualHash != expectedHash {
			return nil, fmt.Errorf("importer: Sources.gz hash mismatch: got %s, want %s", actualHash, expectedHash)
		}

		if _, err := cfg.Store.Blobs.Store(bytes.NewReader(sourcesCompressed)); err != nil {
			l.Warn("failed to cache Sources.gz: %s", err)
		}
	}

	decompressed, err := stream.GzipDecompress(bytes.NewReader(sourcesCompressed))
	if err != nil {
		return nil, fmt.Errorf("importer: decompressing Sources.gz: %w", err)
	}
	sourcesData, err := io.ReadAll(decompressed)
	decompressed.Close()
	if err != nil {
		return nil, fmt.Errorf("importer: reading decompressed Sources: %w", err)
	}

	srcPkg, err := findSourcePackage(sourcesData, packageName)
	if err != nil {
		return nil, fmt.Errorf("importer: %w", err)
	}
	return srcPkg, nil
}

// extractDebianDir creates pkgs/<name>/, extracts the debian/ tree according
// to the package's source format, writes sources.yml, and assembles the
// ImportResult.
func extractDebianDir(cfg ImportConfig, srcPkg *sourcePackage, sourceFiles map[string]objstore.Hash) (*ImportResult, error) {
	pkgDir := filepath.Join(cfg.OutputDir, "pkgs", srcPkg.Name)
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		return nil, fmt.Errorf("importer: creating package directory: %w", err)
	}

	if err := extractDebian(cfg, srcPkg, sourceFiles, pkgDir); err != nil {
		return nil, fmt.Errorf("importer: extracting debian directory: %w", err)
	}

	origEntries := filterOrigEntries(srcPkg, sourceFiles)
	if err := writeSourcesYML(pkgDir, origEntries); err != nil {
		return nil, fmt.Errorf("importer: writing sources.yml: %w", err)
	}

	return &ImportResult{
		Name:    srcPkg.Name,
		Version: srcPkg.Version,
		Format:  srcPkg.Format,
		Sources: origEntries,
	}, nil
}

// sourcePackage holds parsed information about a source package from the Sources index.
type sourcePackage struct {
	Name      string
	Version   string
	Format    string
	Directory string
	Files     []sourceFile
}

// sourceFile represents a single file in a source package's file list.
type sourceFile struct {
	Hash string // SHA-256 hash
	Size string
	Name string
}

// findSourcePackage searches the Sources index data for a package by name.
func findSourcePackage(sourcesData []byte, packageName string) (*sourcePackage, error) {
	reader := deb822.NewReader(bytes.NewReader(sourcesData))

	var best *sourcePackage
	for {
		stanza, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parsing Sources index: %w", err)
		}

		name, ok := stanza["package"]
		if !ok || name != packageName {
			continue
		}

		pkg := &sourcePackage{
			Name:      name,
			Version:   stanza["version"],
			Format:    stanza["format"],
			Directory: stanza["directory"],
		}

		sha256Field, ok := stanza["checksums-sha256"]
		if !ok {
			return nil, fmt.Errorf("package %s missing Checksums-Sha256 field", packageName)
		}

		files, err := parseFileList(sha256Field)
		if err != nil {
			return nil, fmt.Errorf("parsing file list for %s: %w", packageName, err)
		}
		pkg.Files = files

		if best == nil || version.Compare(pkg.Version, best.Version) > 0 {
			best = pkg
		}
	}

	if best == nil {
		return nil, fmt.Errorf("package %q not found in Sources index", packageName)
	}
	return best, nil
}

// parseFileList parses the Checksums-Sha256 field into a list of sourceFiles.
func parseFileList(field string) ([]sourceFile, error) {
	var files []sourceFile
	lines := strings.Split(field, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: <hash> <size> <filename>
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		files = append(files, sourceFile{
			Hash: fields[0],
			Size: fields[1],
			Name: fields[2],
		})
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no files found in hash list")
	}
	return files, nil
}

// downloadSourceFiles downloads all source files for a package and stores them
// in the object store. Returns a map of filename -> objstore.Hash.
func downloadSourceFiles(cfg ImportConfig, pkg *sourcePackage) (map[string]objstore.Hash, error) {
	l := log.From(cfg.Ctx, log.Importer)
	result := make(map[string]objstore.Hash)

	for _, f := range pkg.Files {
		// Check if already in the store
		existing, err := objstore.NewHash(f.Hash)
		if err == nil && cfg.Store.Blobs.Has(existing) {
			l.Debug("cached %s", f.Name)
			result[f.Name] = existing
			continue
		}

		fileURL := fmt.Sprintf("%s/%s/%s", cfg.RepoURL, pkg.Directory, f.Name)
		l.Info("downloading %s", fileURL)
		data, err := httpGet(cfg.HTTPClient, fileURL)
		if err != nil {
			return nil, fmt.Errorf("downloading %s: %w", f.Name, err)
		}

		// Verify the downloaded file's hash
		actualHash := sha256Hex(data)
		if actualHash != f.Hash {
			return nil, fmt.Errorf("hash mismatch for %s: got %s, want %s", f.Name, actualHash, f.Hash)
		}

		// Store in object store
		h, err := cfg.Store.Blobs.Store(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("storing %s in object store: %w", f.Name, err)
		}

		result[f.Name] = h
	}

	return result, nil
}

// filterOrigEntries returns only the orig tarball entries for sources.yml.
func filterOrigEntries(pkg *sourcePackage, files map[string]objstore.Hash) []SourceEntry {
	var entries []SourceEntry
	for _, f := range pkg.Files {
		if isOrigTarball(f.Name) {
			h := files[f.Name]
			entries = append(entries, SourceEntry{
				Name: f.Name,
				Hash: h,
			})
		}
	}
	return entries
}

// isOrigTarball reports whether a filename is an orig tarball. Detached
// upstream signatures (.asc / .sig) sit next to the orig in some packages
// (e.g. coreutils ships coreutils_*.orig.tar.xz.asc) and must NOT be treated
// as orig tarballs — they are not extractable archives, and including them
// in sources.yml would have the build phase try to bind-mount them as
// component origs.
func isOrigTarball(name string) bool {
	if strings.HasSuffix(name, ".asc") || strings.HasSuffix(name, ".sig") {
		return false
	}
	return strings.Contains(name, ".orig.tar.") || strings.Contains(name, ".orig-")
}

// httpGet fetches a URL and returns its body content.
func httpGet(client *http.Client, url string) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("HTTP GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP GET %s: status %d", url, resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response from %s: %w", url, err)
	}
	return data, nil
}

// sha256Hex computes the SHA-256 hex digest of data.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
