# Dependency Locality

**The build chroot for a source package is `lockfile ∪ <local index>`, with local entries winning on name collision. The local index is *not* automatically widened to include every output of every reachable source build — it contains exactly the binaries reached by walking `depends:` and `runtime_depends:` edges. If a sibling carries a `(= same-version)` cross-pin to another sibling that ends up in the local index, you must add the missing edge yourself so both move together.**

## Why

Lockfile entries pin exact runtime versions across a sibling set. A typical Debian source package emits a bundle of binaries — for `pcre2`: `libpcre2-8-0`, `libpcre2-16-0`, `libpcre2-32-0`, `libpcre2-dev`, etc. The `-dev` entry's `Depends:` carries an exact-version pin (`= same-version`) on its runtime sibling. The runtime sibling's `Pre-Depends:` is fuzzier but the mirror's CLI tools are similarly pinned (`bzip2` Depends `libbz2-1.0 (= 1.0.8-6+b2)`).

The merged index is built by name. If only the runtime lib is overlaid locally — `libbz2-1.0` at version `1.0.8-6+gl~…` — the mirror's `bzip2` CLI (which is *not* overlaid) still points at `(= 1.0.8-6+b2)` and the resolver has no candidate that matches. The fix is to overlay the CLI too, so that the local `bzip2` (which `Depends: libbz2-1.0 (= 1.0.8-6+gl~…)`) replaces the mirror entry in the merged index.

The first version of this guideline asked the engine to do this expansion automatically — load every binary from the source manifest whenever any sibling was reached. That was simplified out: the engine now does no implicit sibling expansion, and edges are explicit per-binary.

## How the code actually wires it up

- **`depends:`** is a flat list of `<src>:<bin>` pairs. Each pair becomes a `Depends`/`Includes` edge from every binary this source produces to the named binary. Same-source pairs land in `Includes` (closure-only, no build-order edge); cross-source pairs land in `extraDeps` (real `Depends`).
- **`runtime_depends:`** is a per-binary map; entries follow the same same-source-vs-cross-source split.
- **`makeLocalIndex`** (`internal/build/walk.go`) walks `extraDeps + includes` from the build's `DepArtifacts`, and for each visited binary loads *that one binary* from its source manifest. Siblings of a reached binary are *not* loaded unless they are themselves reached via an explicit edge.
- **Same-source edges are `Includes`**, not `Depends`. The cycle detector ignores them. That's how mutual sibling references (e.g. `libssl3t64` ↔ `openssl-provider-legacy`) and `-dev → runtime-lib` pins coexist with the parent source build being a `Depends` of all its outputs.

## How to apply

When you import a new source whose outputs you'll consume locally, ask:

1. **Which binaries from this source are in *some* build chroot's resolved set?** That includes anything reached via `depends:`/`runtime_depends:` from any other local source, *and* anything that lockfile-pinned mirror binaries reach by `Depends:` after a sibling was overlaid.
2. **Are any of those binaries cross-pinned to siblings of the same source?** If so, add the missing siblings via `runtime_depends:` on the binary that's already in scope.
3. **The most common shapes:**
   - `-dev → runtime-lib`: `libfoo-dev` Depends `libfoo (= same-version)`. If you build `libfoo` locally, add `runtime_depends: { libfoo-dev: [src:libfoo] }`.
   - `-dev → sibling CLI`: if the source produces a CLI tool that mirror packages pull in transitively (`build-essential → dpkg-dev → bzip2 → libbz2-1.0`), and the runtime lib is also a local sibling, add the CLI to the same `runtime_depends` entry: `libbz2-dev: [bzip2:libbz2-1.0, bzip2:bzip2]`. Same-source edges stay closure-only, so the CLI in the rootfs is then `narrowInstallSet`'s job, not an extra `Depends`.
   - **Mutual sibling cycle**: list both directions (`libssl3t64 ↔ openssl-provider-legacy`). The engine handles this via `Includes`.
4. **When an apparent solver failure looks like "package X depends on Y version Z, Z not found":** the local index has Y at a different version and X (mirror) at the version that pins to mirror-Y. Find X's source build and add the missing edge so X moves to local too.

## See also

- [Concept: lockfiles](../concepts/lockfiles.md) — the version-pinning model.
- [Writing build.yml](../guide/build-yml.md) — the user-facing surface for the edges this rule cares about.
- [`internal/build/walk.go`](../internals/build/build.md) — `walkBinaries` and `makeLocalIndex`.
- [Identity and Caching](./identity-and-caching.md) — locality is a property *of* the input set; identity is the hash *of* it.
