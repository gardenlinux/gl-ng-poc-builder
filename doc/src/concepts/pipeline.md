# End-to-End Pipeline

This page ties the previous concept pages together by walking the full pipeline that produces a usable rootfs from nothing.

## The four phases

```mermaid
flowchart LR
    import["1. Import
    gl import"]
    lock["2. Lock
    gl lockfile"]
    build["3. Build
    gl build"]
    use["4. Use
    gl exec-chroot"]
    import --> lock --> build --> use
```

Each phase is independently invokable. Each writes its outputs into the conf-dir or the object store; the next phase reads from there. There is no implicit shared state between phases except the on-disk artifacts.

## Phase 1: Import

Goal: get the sources for one Debian package into the conf-dir.

```mermaid
sequenceDiagram
    participant User
    participant CLI as gl import
    participant Mirror as deb.debian.org
    participant Store as Object store
    participant Conf as conf-dir/pkgs/[name]/

    User->>CLI: gl import -output ./conf -package bash
    CLI->>Mirror: GET InRelease
    CLI->>CLI: GPG verify (or skip with -no-verify)
    CLI->>Mirror: GET Sources.gz
    CLI->>CLI: pick latest version of bash
    CLI->>Mirror: GET .dsc, .orig.tar.*, .debian.tar.*
    CLI->>CLI: detect format (3.0 quilt / 3.0 native / 1.0)
    CLI->>Conf: extract debian/ to src/debian/
    CLI->>Store: store orig tarball as blob
    CLI->>Conf: write sources.yml referencing blob
```

What's on disk after import:

```
conf-dir/pkgs/bash/
├── sources.yml          # references orig tarball blob
└── src/
    └── debian/          # control, rules, patches, changelog, ...
```

Plus, in the object store: one or more `orig.tar.*` blobs.

## Phase 2: Lock

Goal: pin the *build-time* dependencies for one package.

```mermaid
sequenceDiagram
    participant User
    participant CLI as gl lockfile
    participant Mirror as deb.debian.org
    participant Resolver
    participant Store as Object store
    participant Conf as conf-dir/pkgs/[name]/

    User->>CLI: gl lockfile -package bash
    CLI->>Conf: read src/debian/control + build.yml
    CLI->>CLI: extract Build-Depends, filter by arch + profiles
    CLI->>Mirror: GET Packages.gz
    CLI->>Resolver: resolve(essentials ∪ Build-Depends)
    Resolver-->>CLI: closure (set of binary packages + versions)
    CLI->>Mirror: download each .deb
    CLI->>Store: store .debs as blobs
    CLI->>CLI: serialise closure as deb822 (sorted)
    CLI->>Store: store as blob, get hash H
    CLI->>Conf: write build-deps.yml: "[arch]: H"
```

What's on disk after lock:

```
conf-dir/pkgs/bash/
├── ...
├── build.yml            # was already there (hand-edited)
└── build-deps.yml       # new — references the lockfile blob
```

Plus: every binary `.deb` referenced by the lockfile is now in the object store.

The lockfile is what makes the build phase hermetic: every byte that goes into the build chroot from "the Debian mirror" was fetched at lock time.

## Phase 3: Build

Goal: produce, from `pkgs/<name>`, a set of locally-built `.deb`s — and recursively, do this for everything anyone in the rootfs depends on.

This is the only phase that exercises the artifact graph engine. The CLI receives a top-level target — typically `rootfs:gl-rootfs` — and the engine walks its dependency closure, producing each `DebianPkgBuild` and `BinaryPkg` artifact in order.

```mermaid
flowchart TD
    cli["gl build rootfs:gl-rootfs"]
    discover["Engine discovers transitive deps
    via Depends() / Includes()"]
    schedule["Schedule artifacts in
    topological order"]
    pkgbuild["For each DebianPkgBuild:
    assemble chroot, run dpkg-buildpackage,
    collect .debs"]
    pkgvalidate["For each BinaryPkg:
    install-check, locality-check"]
    rootfsbuild["For Rootfs:
    overlay-mount layers,
    dpkg --unpack/configure,
    tar -czf"]
    out["Final rootfs.tar.gz blob
    stored in objstore"]

    cli --> discover --> schedule --> pkgbuild --> pkgvalidate --> rootfsbuild --> out
```

Two ideas to keep in mind from the previous pages:

- **Caching is by identity**, so a re-run with no input change is a no-op. The engine touches the cache once per artifact, sees `map[id]` already populated, and skips straight to the output manifest.
- **Locality is checked at the binary level**, not at the rootfs level, so the moment you accidentally introduce a runtime dependency you don't build from source, the offending `BinaryPkg.Build()` fails. The rootfs is implicitly local because all its transitively-depended `BinaryPkg` artifacts had to pass.

What the build phase produces:

- One `.deb` blob per binary output of every source build.
- One control-text blob per binary.
- One manifest blob per source build (mapping name → blob hash).
- One manifest blob per `BinaryPkg` (a tiny pointer to the parent's outputs).
- One `rootfs.tar.gz` blob.
- One identity-mapped manifest entry for the rootfs.

## Phase 4: Use

Goal: do something with the rootfs.

The simplest "use" is `gl exec-chroot`:

```mermaid
sequenceDiagram
    participant User
    participant CLI as gl exec-chroot
    participant Store as Object store
    participant Container as Container layer

    User->>CLI: gl exec-chroot [rootfs-id] bash -c 'ls -lah /'
    CLI->>Store: lookup rootfs identity → manifest hash
    CLI->>Store: parse manifest → tar.gz blob hash
    CLI->>CLI: extract tar.gz to tmpdir
    CLI->>Container: assemble UserNS+MountNS+Container around tmpdir
    Container->>Container: pivot_root, mount /proc, /sys, /dev
    Container->>Container: exec bash -c 'ls -lah /'
    Container-->>User: stdout/stderr stream
```

This proves the rootfs is functional — bash starts, `ls` works, and (in the integration test) there are zero non-locally-built binaries on disk.

Other "uses" — packing as a disk image, an OCI image, or installing via PXE — fit on top of this same starting point. Phase 1 ships only the tar.gz form and the exec-chroot reuse.

## A complete worked example

The `tests/full_build_test.sh` script is exactly this pipeline, from scratch:

```mermaid
flowchart TB
    p1["prepare_staging.sh:
    1. mkdir -p conf-dir/pkgs/&lt;name&gt;/
    2. copy build.yml from tests/templates/
    3. for each package:
       gl import -package &lt;name&gt;
       gl lockfile -package &lt;name&gt;
    4. gl lockfile-rootfs"]
    p2["full_build_test.sh:
    5. gl build rootfs:gl-rootfs
    6. gl exec-chroot &lt;hash&gt; bash -c 'ls /'"]

    p1 --> p2
```

The whole thing — bash, glibc, coreutils, gcc-16's libgcc-s1, openssl, systemd, gmp, zlib, libzstd, libcap2, base-files, base-passwd — runs unattended in a few minutes on the development VM (64 cores, ample RAM). The output is a rootfs tarball whose every `.deb` was built locally inside our own container infrastructure.

## What the next chapter covers

Everything above is at the conceptual level. The next chapter — User Guide — walks the same pipeline as a sequence of CLI invocations, with concrete file syntax, flags, and the conventions for setting up a conf-dir.

## See also

- The CLI walkthrough: [End-to-End Walkthrough](../guide/e2e.md).
- The implementation: [Build System internals](../internals/build/index.md).
- Source management beyond Phase 1: [Source Management and the Staging Repo](./sources.md).
