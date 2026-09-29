# Generating Lockfiles

Lockfiles pin the build-time tooling for a package — exactly which `.deb`s are installed in the build chroot when `dpkg-buildpackage` runs. There are two CLI commands:

- `gl lockfile` — per-package lockfile, capturing build-deps for one source package.
- `gl lockfile-rootfs` — rootfs-scope lockfile, capturing the Debian tooling used during rootfs configuration.

Both write a `<arch>: <hash>` pointer file into the conf-dir; the actual lockfile content lives as a blob in the object store.

## `gl lockfile`

```text
gl lockfile [flags] <package-name>

Flags:
  --repo string       APT repository URL (default "https://deb.debian.org/debian")
  --dist string       Distribution (default "testing")
  --arch string       Target architecture (default "amd64")
  --cache string      Object store directory (default ~/.cache/gl-ng)
  --output string     Conf-dir root (default ".")
  --cookie string     InRelease cache cookie
```

Example:

```bash
gl lockfile --output ./staging --cookie "$COOKIE" bash
```

Writes `./staging/pkgs/bash/build-deps.yml`:

```yaml
amd64: 9209690faf8b2b09cb02be917387a121be291af320548a1600b5105bc646be53
```

Plus, in the object store:
- One blob containing the deb822-formatted `Packages` index of the lockfile.
- One blob per `.deb` referenced by the lockfile (downloaded eagerly so build time never hits the network).

## What goes into the lockfile

The build-deps the lockfile resolves are:

1. **Essential packages.** Every package in Debian's `Essential: yes` set. Always present.
2. **Implicit build prerequisites.** `build-essential`, `fakeroot`, `debconf`. Always added.
3. **The package's `Build-Depends`, `Build-Depends-Arch`, and `Build-Depends-Indep`** from `pkgs/<name>/src/debian/control`. All three lists are honoured — `dpkg-buildpackage` checks all of them.

Each dep atom is filtered by:

- **Architecture restrictions.** `hurd-dev [hurd-any]` is dropped on `amd64`. Architecture wildcards (`linux-any`, `any-cpu`) are honoured.
- **Build profiles.** Atoms with `<!nobiarch>` are excluded when `nobiarch` is in `build_profiles:`. Conversely, atoms with `<nocheck>` are excluded when `nocheck` is in `build_profiles:`. The exact rule follows Debian policy: a dep is included iff at least one of its restriction groups evaluates to true.

The active build profiles are read from `pkgs/<name>/build.yml`'s `build_profiles:` field. The lockfile generator and the build runtime *must* see the same profile set, otherwise the lockfile is generated for a different chroot than the build expects.

Once the dep set is filtered, the [resolver](../internals/debian/resolver.md) computes a transitive closure against the mirror's `Packages` index. The closure is then serialised back as deb822 and stored.

## `gl lockfile-rootfs`

```text
gl lockfile-rootfs [flags]

Flags:
  --repo string       APT repository URL
  --dist string       Distribution
  --arch string       Target architecture
  --cache string      Object store directory
  --output string     Conf-dir root
  --cookie string     InRelease cache cookie
```

Example:

```bash
gl lockfile-rootfs --output ./staging --cookie "$COOKIE"
```

Writes `./staging/rootfs-deps.yml`. Contents are the *Debian tooling* (essentials + `perl-base` + `mawk`), no APT — these are the tools used to run maintainer scripts during rootfs configuration. They are present on Layer 1 of the rootfs assembly overlay and absent from the final image.

There is no `<package-name>` argument; the inputs are fixed. (See [Concept: Lockfiles](../concepts/lockfiles.md) for why APT is excluded.)

## Determinism

Re-running `gl lockfile` against the same Debian mirror state produces an identical blob hash. Two design points keep this true:

1. **Resolver ranking** is deterministic — virtual-package providers and version candidates sort by a stable rule.
2. **Stanza emission** sorts each stanza's keys before writing, since Go `map` iteration is randomized.

If the mirror has moved on (testing is rolling), you'll get a different hash. That's expected — the lockfile *is* a snapshot of the mirror.

## When to regenerate

A lockfile *needs* to change when:

- Your `build_profiles:` changes (different filter result).
- Your `src/debian/control`'s `Build-Depends:` changes (you imported a new upstream version).
- The mirror has updated and you want to refresh.

Re-run the same `gl lockfile <name>` command; the new blob hash is written to `build-deps.yml`. Phase 1 does *not* auto-refresh lockfiles on every mirror tick — that would generate noise.

## When lockfile generation fails

Debian testing is mid-migration, sometimes for weeks. If a package's build-deps are unsatisfiable in the current state of testing, lockfile generation fails. The Phase 2 vision is to retry on the next automation tick. In Phase 1 the manual recovery is to wait, switch to `--dist unstable`, or pin specific versions. See [Concept: Lockfiles — Lifecycle](../concepts/lockfiles.md).

## See also

- [Concept: Lockfiles](../concepts/lockfiles.md) — why lockfiles exist and what they pin.
- [Writing build.yml](./build-yml.md) — `build_profiles:` and other fields the generator reads.
- [Lockfile internals](../internals/build/lockfile.md) — implementation details.
- [Resolver internals](../internals/debian/resolver.md) — the dependency solver.
