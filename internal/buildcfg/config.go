// Package buildcfg holds the on-disk YAML schemas (build.yml, sources.yml,
// rootfs.yml, build-deps.yml, rootfs-deps.yml) and their typed loaders.
//
// It exists as a neutral configuration package so that internal/build and
// internal/lockfile can both consume it without one having to import the
// other.
package buildcfg

import (
	"os"
	"path/filepath"

	"gl-ng/internal/objstore"
	"gopkg.in/yaml.v3"
)

// BuildYML holds per-package build configuration loaded from build.yml.
type BuildYML struct {
	BuildProfiles []string `yaml:"build_profiles"`
	BuildOptions  []string `yaml:"build_options"`
	ExtraBuildEnv []string `yaml:"extra_build_env"`
	// Depends lists src:pkg pairs that this source build depends on.
	// These feed into build-dep resolution via local index merge AND are added
	// to every binary package's runtime deps (for rootfs transitive closure).
	Depends []string `yaml:"depends"`
	// RuntimeDepends maps binary package name → list of src:pkg pairs that are
	// runtime dependencies of that specific binary.
	RuntimeDepends map[string][]string `yaml:"runtime_depends"`
	// LockfileDeps maps binary package name → list of package names that should
	// be pulled from the lockfile during that binary's install validation check.
	LockfileDeps map[string][]string `yaml:"lockfile_deps"`
}

// LoadBuildYML reads and parses a build.yml from a package directory. Returns
// the zero value if the file is missing or malformed.
func LoadBuildYML(pkgDir string) BuildYML {
	var out BuildYML
	data, err := os.ReadFile(filepath.Join(pkgDir, "build.yml"))
	if err != nil {
		return out
	}
	_ = yaml.Unmarshal(data, &out)
	return out
}

// SourcesYML is the on-disk shape of pkgs/<name>/sources.yml.
type SourcesYML struct {
	Sources []sourceEntry `yaml:"sources"`
}

type sourceEntry struct {
	Name string `yaml:"name"`
	Hash string `yaml:"hash"`
}

// SourceRef is the in-memory shape: a source-tarball name and its
// already-validated objstore hash.
type SourceRef struct {
	Name string
	Hash objstore.Hash
}

// LoadSourcesYML reads sources.yml from a package directory and returns the
// list of source-tarball references.
func LoadSourcesYML(pkgDir string) ([]SourceRef, error) {
	data, err := os.ReadFile(filepath.Join(pkgDir, "sources.yml"))
	if err != nil {
		return nil, err
	}
	var raw SourcesYML
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	refs := make([]SourceRef, 0, len(raw.Sources))
	for _, e := range raw.Sources {
		ref := SourceRef{Name: e.Name}
		if e.Hash != "" {
			if h, err := objstore.NewHash(e.Hash); err == nil {
				ref.Hash = h
			}
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// RootfsYML is the on-disk shape of <staging>/rootfs.yml.
type RootfsYML struct {
	Packages []string `yaml:"packages"`
}

// LoadRootfsYML reads packages from rootfs.yml in the given directory.
// Returns nil if the file is missing or malformed.
func LoadRootfsYML(dir string) []string {
	data, err := os.ReadFile(filepath.Join(dir, "rootfs.yml"))
	if err != nil {
		return nil
	}
	var raw RootfsYML
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil
	}
	return raw.Packages
}

// LoadArchHashYML reads a "<arch>: <hash>"-shaped lockfile pointer file
// (build-deps.yml, rootfs-deps.yml) and returns the hash for the requested
// architecture.
func LoadArchHashYML(path, arch string) (objstore.Hash, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return objstore.Hash{}, err
	}
	var m map[string]string
	if err := yaml.Unmarshal(data, &m); err != nil {
		return objstore.Hash{}, err
	}
	hashStr, ok := m[arch]
	if !ok {
		return objstore.Hash{}, &archNotFoundError{arch: arch, path: path}
	}
	return objstore.NewHash(hashStr)
}

type archNotFoundError struct {
	arch string
	path string
}

func (e *archNotFoundError) Error() string {
	return "no lockfile hash for arch " + e.arch + " in " + e.path
}
