# Writing `rootfs.yml`

The `rootfs.yml` at the root of the conf-dir lists the *direct* binaries to install in the rootfs. The transitive runtime closure is computed automatically by the engine.

## Anatomy

```yaml
packages: [base-files:base-files, base-passwd:base-passwd, coreutils:coreutils, bash:bash, gcc-16:libgcc-s1, dpkg:dpkg]
```

One field — `packages:` — and a flat list of `<source-package>:<binary-package>` pairs.

That's the entire grammar. There is no second list for "implicit base packages." There is no per-package configuration. There is no machinery for selecting flavours or excluding components. Phase 1 ships exactly this.

## How the closure is computed

When the engine builds the artifact graph for `rootfs:gl-rootfs`, it:

1. Resolves each `<src>:<bin>` pair to a `BinaryPkg` artifact in the graph.
2. For each `BinaryPkg`, recursively walks its `Depends` and `Includes` (the edges produced by `build.yml`'s `depends:` and `runtime_depends:`).
3. The full reachable set of `BinaryPkg` artifacts becomes the rootfs's `Inputs()` — every `.deb` to extract and configure.

You do *not* write `libc6` in `rootfs.yml`. It appears because something — `coreutils`, `bash`, `gcc-16:libgcc-s1` — transitively depends on it.

If a base package would be missing from the closure (say, you forgot something that nothing else pulls in), the build either:

- Fails the locality check, because some `BinaryPkg` has an unsatisfied dependency that's not in `lockfile_deps:`, or
- Fails the install validation, because `dpkg --configure` complains about a missing `Pre-Depends`.

Both are deterministic. Neither is silent.

## What goes in the list

The right mental model is: **the entry points to your runtime graph**. Anything you'd `apt-get install` first if you were building a Debian system from scratch. Beyond that, the closure is automatic.

For the e2e test:

```yaml
packages: [base-files:base-files, base-passwd:base-passwd, coreutils:coreutils, bash:bash, gcc-16:libgcc-s1, dpkg:dpkg]
```

- `base-files`, `base-passwd` — Debian's lowest-level skeleton (`/etc/passwd`, `/etc/os-release`, etc.).
- `coreutils` — `ls`, `cat`, `mv`, etc.
- `bash` — interactive shell.
- `gcc-16:libgcc-s1` — runtime support library for compiled binaries. Pulls in nothing else from gcc-16 the way the graph is set up; just the one binary.
- `dpkg:dpkg` — the package manager itself, shipping in Layer 0. Its source build also produces `dpkg-dev`, `dselect`, `libdpkg-perl`, `libdpkg-dev`, but only `dpkg` is named here, so the others are unreachable graph nodes that the engine never validates. Pulls `tar`, `libbz2-1.0`, `liblzma5`, `libmd0` into the closure, all of which are in turn produced by local sources (`tar`, `bzip2`, `xz-utils`, `libmd`).

## What does *not* go in the list

- Build-time tooling like `gcc`, `make`, `dpkg-dev` — these belong in build chroots (handled by lockfiles), not in the rootfs.
- The Debian tooling required for rootfs configuration (`dpkg`, `perl-base`, `mawk`) — these come from `rootfs-deps.yml`, the rootfs lockfile. They're present during configuration and discarded before the final tar.
- Transitive deps of anything you've already listed.

## The naming scheme

`<source-package>:<binary-package>` is a hard requirement. Even when source name and binary name match (the common case for trivial packages like `bash`), you write both: `bash:bash`. Reasons:

1. Disambiguating siblings. `gcc-16` produces `libgcc-s1`, `gcc-16`, `gcc-16-base`, ... — `gcc-16:libgcc-s1` selects exactly one.
2. Engine clarity. The src side identifies the `DebianPkgBuild` artifact; the bin side identifies the `BinaryPkg` artifact.

## Editing in practice

The edit cycle is:

1. Add an entry to `rootfs.yml`.
2. If the package isn't yet in `pkgs/`, run `gl import <source-package>` and write a `build.yml` for it.
3. Run `gl lockfile <source-package>`.
4. Run `gl build`.

If `gl build` complains about a missing `BinaryPkg` (because some transitive dep wasn't imported), import that one too and repeat.

## See also

- [Concept: Rootfs](../concepts/rootfs.md) — the three-layer overlay model and what's in the final image.
- [Writing build.yml](./build-yml.md) — how the per-package files contribute to the rootfs's transitive closure.
- [Generating Lockfiles](./lockfile.md) — `lockfile-rootfs` for the configuration-time tooling.
