# Vision and Design Principles

GardenLinux is an *image-centric, immutable* operating system. Its primary deliverable is a finished system image — a Kubernetes node, a VM host, a container base — where every installation, configuration, and version pin is decided at build time. Once shipped, the image does not mutate. There is no `apt-get install` at runtime, no implicit trust in package mirrors, no drift between what you tested and what's running.

`gl-ng` exists to produce those images. To do that well, it borrows from two ecosystems that are normally treated as incompatible:

| From Debian | From Nix-style systems |
|-------------|------------------------|
| Massive ecosystem of mature packages | Pure, content-addressed builds |
| `dpkg`-managed file ownership | Build isolation as a first-class invariant |
| Familiar FHS layout, glibc, GNU userland | Reproducibility by construction |
| Stable runtime ABI conventions | No global mutable state |

`gl-ng` keeps Debian's runtime and ecosystem leverage, and adopts Nix's discipline around inputs, identity, and caching. It does *not* adopt Nix's store layout or Debian's APT-as-source-of-truth model.

## Five design principles

### 1. The artifact graph is the truth

Everything that gets built is an *artifact*. An artifact is the smallest unit that can be cached and depended on. The graph of artifacts is declared explicitly: there are no implicit dependencies, no scanning of the host system, no "build whatever satisfies this name". If two builds disagree about what they consume, the graph is wrong; the graph is fixed first, then the build runs.

### 2. Identity is content-derived

Every artifact has an *identity hash* computed from its inputs: the hash of its source tree, the architecture, the identities of its dependencies. Identities are not assigned, they are derived. Two artifacts with the same identity *are* the same artifact, even if they were built on different machines years apart. This is what makes the cache safe: a hit is not "probably the same" — it is the same.

### 3. The object store is the only persistent state

There is exactly one place where build outputs live: the object store, a flat content-addressed directory of blobs. There is no APT repository, no separate build cache, no per-developer scratch dir that other tools care about. Any blob the system might want — orig tarballs, lockfile indexes, `.deb` files, rootfs tarballs — lives in `blobs/`, addressed by SHA-256.

A second directory, `map/`, indexes from artifact identities to blob hashes. That's it. Two directories. Everything else is derived.

### 4. Isolation is a kernel concern, not a runtime concern

`gl-ng` does not call out to Docker, Podman, systemd-nspawn, bubblewrap, or anything else. Build isolation is implemented directly with Linux namespaces (`CLONE_NEWUSER`, `CLONE_NEWNS`, `CLONE_NEWPID`) and a small Go-native runtime. This avoids a class of supply-chain and reproducibility issues, lets the system run as an unprivileged user via subordinate UID ranges, and keeps the dependency footprint minimal.

### 5. Phase 1 only solves the build half

The original architecture envisions automated import from Debian, version-tracked package branches in Git, snapshot-based history, release branches, and an embedded APT-served runtime index for container base images. `gl-ng` Phase 1 implements the build half: from a monorepo of imported sources, produce a rootfs entirely from source. The Git automation, multi-release model, and runtime APT index are conceptually described in this chapter and in [Source Management](./sources.md), but Phase 1 deliberately stops at "from-source rootfs that boots and runs `bash`".

## What "reproducible" means here, precisely

Reproducibility is bounded by the underlying packages. If `dpkg-buildpackage` for a particular source embeds a build timestamp into the binary, no wrapper can prevent that. The architecture guarantees that the *inputs* to the build are deterministic:

- The source tree hash is deterministic (see [`dirhash`](../internals/foundations/dirhash.md)).
- The set of build-time tooling is frozen by the lockfile (see [Lockfiles](./lockfiles.md)).
- The artifact dependencies are pinned by their content-derived versions.
- The container environment is constructed from the merged set with no host leakage.

If a particular package still produces non-deterministic output despite identical inputs, that's a property of that package, not of `gl-ng`. The hash of its `.deb` will then not match across runs — which the caching layer correctly detects as a different artifact.

## What "from source" means here, precisely

Two distinct populations of `.deb` files exist during a build:

- **Build chroot tooling**: the compilers, `debhelper`, library `-dev` headers used *during* `dpkg-buildpackage`. These come from the Debian mirror via the lockfile. They are tools; they end up in nobody's image.
- **Output binaries**: every `.deb` produced by our source builds and consumed downstream — including by the rootfs. These must, transitively, be entirely buildable from sources we have imported.

The rootfs assembly is the gate. If the rootfs's transitive runtime closure includes a binary we did not build, the build fails. There is no mechanism to fall back to the mirror for a runtime dependency. This is enforced by the [locality check](../internals/build/build.md#binarypkg-validation) in the binary package artifact.

## Why a monorepo, not many repos

Per-package repos make automation harder, lockfile coordination harder, and atomicity impossible: you cannot bump `glibc` and the things that depend on it in a single reviewable change. A single repository with one directory per package — and one merge of "package branch" → main per upstream import — gives:

- An atomic snapshot of the world for any commit.
- A single `git log` showing all upstream imports interleaved with local changes.
- A single CI run that builds the whole thing and tells you whether the world is consistent.

Phase 1 ships the monorepo layout but defers the per-package-branch automation; see [Source Management](./sources.md).

## What gets re-implemented vs. what gets reused

| Reused as-is | Reimplemented in Go |
|--------------|---------------------|
| `dpkg`, `dpkg-deb`, `apt`, `dpkg-buildpackage` (run inside the build chroot) | Source import, lockfile generation, dependency resolution |
| `xz`, `gzip`, `bzip2`, `zstd`, `tar` (called as subprocess pipes) | Object store, content addressing, cache lookups |
| `gpgv` (subprocess, for signature verification) | Container runtime (user/mount/PID namespaces) |
| Debian's package format and protocol | Artifact graph engine, parallel scheduler |

The principle is simple: anything that is policy or coordination (the graph, the cache, the resolver, isolation) is owned by `gl-ng`. Anything that is specialized labour with a long history (compression, signature math, the dpkg toolchain itself) is delegated.
