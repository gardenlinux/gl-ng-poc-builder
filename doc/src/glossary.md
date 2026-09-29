# Glossary

Terms and short names that appear throughout this documentation, alphabetized. If you only have time to learn ten, **artifact identity**, **lockfile**, **manifest**, **conf-dir**, **stub binary**, **MountNS**, **rootfs (3-layer)**, **gl~ version**, **locality check**, and **Includes vs. Depends** are the highest-leverage.

---

**`apt`**
Debian's high-level package manager. gl-ng does not run apt at the build-system level — only inside source-build chroots (where it pulls in build-deps). Rootfs assembly is dpkg-only on purpose; `apt` is excluded from the rootfs lockfile.

**Artifact**
A unit of work in the build engine. Implements the `artifact.Artifact` interface — has a `Key()`, `Identity()`, `Depends()`, `Includes()`, `Inputs()`, `Build()`. The three concrete artifact types are `DebianPkgBuild`, `debianBinaryPkg`, and `Rootfs`.

**Artifact identity** (or just **identity**)
A 256-bit hash derived from everything that affects an artifact's outputs: source-tree hash, dependency identities, target arch, etc. Identity is the cache key. Same identity → same outputs.

**`base-passwd`, `base-files`, `debconf`**
The seed packages that must be force-installed before APT can run inside a build chroot.

**Binary package**
A `.deb` containing built artifacts. In gl-ng's graph, represented by the `debianBinaryPkg` artifact — a *validation gate* over a name within a parent source build.

**Build chroot**
The throwaway filesystem inside which `dpkg-buildpackage` runs for a single source build. Lives on a tmpfs inside `MountNS`, populated by extracting all build-deps + bind-mounting source + tarballs. Discarded when the build finishes.

**`build-deps.yml`**
The per-package lockfile pointer file. Tiny `<arch>: <hash>` map pointing at the lockfile blob in the object store. Lives at `pkgs/<n>/build-deps.yml` in the conf-dir.

**`build.yml`**
Per-package build configuration in the conf-dir. Carries `depends:`, `runtime_depends:`, `lockfile_deps:`, `extra_build_env:`, `build_profiles:`, etc.

**Cache**
Synonym for the **object store**. The `--cache` flag and `GL_CACHE_DIR` env var both refer to its root path.

**Conf-dir**
The directory containing a buildable configuration: `pkgs/`, `rootfs.yml`, lockfile pointer files. `gl build` finds the nearest ancestor containing `pkgs/` + `rootfs.yml`. Also called the *staging tree* in older docs.

**Container** (gl-ng-internal)
The innermost layer of the namespace stack — adds PID namespace and `pivot_root` on top of `MountNS`. The thing that actually runs `dpkg-buildpackage`. Not to be confused with Docker/OCI containers, which gl-ng does not use.

**Cookie**
A user-supplied label (e.g., `--cookie 2026-05-12`) that pins an InRelease blob in the object store. Subsequent imports/lockfile runs with the same cookie reuse the same upstream snapshot, even if Debian testing has moved on.

**`Depends()` (artifact)**
The build-order edges of an artifact. `A.Depends(B)` means B must finish (cache hit or successful build) before A can start. Cycles forbidden. Different from Debian's `Depends:` field, though related.

**`debianBinaryPkg`**
The validation-gate artifact type. Doesn't compile anything — confirms a binary came from its parent source build, validates locality, runs an install check. Output is a passthrough of the parent's `.deb`.

**`DebianPkgBuild`**
The source-build artifact type. The heaviest artifact: builds a .deb from a Debian source package via `dpkg-buildpackage` in a fresh container chroot.

**deb822**
Debian's stanza-based key-value file format (RFC 822-style). Used for control files, Sources/Packages indices, Release files.

**`dirhash`**
The deterministic directory hashing primitive. Uses `os.OpenRoot` (Go 1.24+) for safe symlink-bounded traversal. Output is byte-stable across reorders, irrelevant timestamp changes, and host-specific noise.

**`dpkg --unpack` + `--configure --pending`**
The two-phase install pattern used everywhere in gl-ng: first unpack a closed set of debs without resolving Pre-Depends order, then configure them all in dependency order. Bypasses the chicken-and-egg problem of bootstrapping circular essential packages.

**`gl~` version**
The synthetic version suffix gl-ng injects into produced .debs: `<base>+gl~<srcHash[:8]>`. Sorts above the base Debian version, content-derived.

**Identity**
See **Artifact identity**.

**`Includes()` (artifact)**
Closure-only edges. `A.Includes(B)` does NOT mean A waits for B — but if `C.Depends(A)`, the engine ALSO orders C after B (and B's transitive includes). Cycles legal. Used to model same-source sibling binaries that must always travel together without creating false ordering.

**`Inputs()` (artifact)**
Concrete output references — "I consume the `control:libc6` output of node N." Resolved after all `Depends` are complete and just before `Build()`. Resolution is **exact match only**: source builds emit stable names like `libc6.deb`, so a consumer asking for `libc6.deb` matches directly. Versioned filenames (`libc6_2.41-12+gl~hash_amd64.deb`) live inside the `.deb` archive, not in the artifact graph.

**InRelease**
The signed top-level metadata file from a Debian APT repository. Contains SHA256 hashes of every other file in the repo. The chain of trust starts here.

**Layer 0 / Layer 1 / Layer 2**
The three tiers of the rootfs overlay. **Layer 0**: locally-built `.debs`, runtime closure only. **Layer 1**: lockfile-fetched Debian tooling needed by postinst scripts (perl-base, dpkg, etc.) — discarded from the final image. **Layer 2**: overlay upper dir, captures dpkg-status mutations, alternatives, ldconfig output.

**Locality check**
The validation step in `debianBinaryPkg.Build` that ensures every runtime dep of a binary package can be satisfied either from the local build set OR from explicit `lockfile_deps:` allow-listed escape hatches. The hard line that guarantees the rootfs is from-source.

**Lockfile**
A frozen, deb822-formatted snapshot of resolved build-deps (or rootfs Layer-1 deps), stored as a blob in the object store. Pointed at by a per-arch `<arch>: <hash>` line in `build-deps.yml` or `rootfs-deps.yml`.

**`lockfile_deps:`**
A per-binary allow-list in `build.yml` for runtime deps that the install-check is permitted to resolve from the lockfile. Used for leaf packages we can't reasonably build (e.g., `linux-libc-dev`, `rpcsvc-proto`) and for shlibs-introduced runtime libs not wired into the explicit graph (e.g., `libgcc-s1`). Strictly per-binary — does not cascade to consumers.

**Map**
The object-store sub-store mapping `identity → manifest_hash`. The artifact engine's cache lookup is `store.Map.Has(identity)`.

**Manifest** (artifact-engine sense)
A blob containing one `<hash> <output-name>\n` line per output an artifact produced. The map points at this manifest, not at any single output. Consumers look up outputs by name.

**MountNS**
The mount-namespace layer of the ExecEnv stack. Provides mount isolation. Mounts done here propagate into a subsequent `Container` if `MS_SHARED` is set.

**Object store**
The on-disk content-addressed store at `~/.cache/gl-ng/` (or `--cache` path). Three sub-stores: `blobs/` (the actual content-addressed blobs), `map/` (identity→manifest pointers), `sources/` (GC-pinned orig tarballs).

**Orig tarball**
The upstream-pristine source archive of a Debian package (`.orig.tar.{gz,xz,bz2}`). Marked as a Source blob in the object store — survives garbage collection.

**`PackageSet`**
The registry of all source builds in a conf-dir. Indexes binaries-by-name, virtuals-via-Provides, and provides-per-binary. Used by `validateLocality` and `BuildGraph`.

**`pkgs/<n>/`**
A package directory in the conf-dir. Contains `build.yml`, `build-deps.yml`, `sources.yml`, and `src/` (the imported Debian source tree).

**Pin** (object-store sub-store)
A GC root for local-only input blobs, stored one YAML file per pin at `pins/<pin-id>.yml`. `gl import` creates a source pin (orig tarballs); `gl lockfile` creates a build-deps pin (index blob + `.deb`s). GC keeps any pinned blob. Manage with `gl cache pin list/show/drop`. Replaces the old `Sources` sub-store.

**Resolver**
The DPLL-style backtracking dependency solver in `internal/resolver`. Used by both lockfile generation (against upstream Packages) and install checks (against the local build set).

**Rootfs**
The final filesystem image artifact. Output is a `rootfs.tar.gz` blob built via the 3-layer overlay assembly.

**`rootfs-deps.yml`**
The rootfs-wide lockfile pointer file. Same format as `build-deps.yml` but at the conf-dir root.

**`rootfs.yml`**
The conf-dir-root file listing the binary packages the rootfs should contain. Each line is a `src:pkg` reference. The full closure (transitively through `runtime_depends:` and `Includes`) is what actually goes into the image.

**`sources.yml`**
Per-package list of orig tarball references. `name: ...` + `hash: ...` per entry. Used by the build system to locate and bind-mount tarballs into the build chroot.

**Staging tree**
Same as **conf-dir**. Older term, occasionally still used.

**Stub binary** (`exec_env_stub`)
The small Go binary that becomes PID 1 inside each ExecEnv layer. Receives `ExecRequest` messages over IPC, performs the namespace setup, fork/execs the requested command. The single privileged operation in the system — everything else delegates to it.

**`TaskTracker`** / **`TaskOverview`** / **`NonInteractiveViewer`**
The taskui package. Tracks per-artifact build state, renders TUI overviews on TTYs, falls back to line-by-line output otherwise. Backs `gl build`'s real-time UI and the `--view-logs` replay.

**`UserNS`**
The user-namespace layer of the ExecEnv stack. Maps the calling user's uid to inner uid 0, plus 65535 subordinate ids. Lets the rest of the stack do "root" things (mount, pivot_root, chown) without actual host root.

**Virtual package**
A package name listed in another package's `Provides:` field. `awk` is virtual, provided by `mawk` and `gawk`. The resolver and locality check both follow these.
