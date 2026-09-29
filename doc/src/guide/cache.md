# Managing the Cache

The object store at `~/.cache/gl-ng` (or wherever `--cache` / `GL_CACHE` points) is where every blob lives — orig tarballs, lockfile indexes, `.deb` blobs, source-build manifests, and the rootfs `.tar.gz`. It is content-addressed and append-only at the blob level; the `map/` directory carries the identity → manifest pointers that drive cache hits, and `pins/` holds the GC roots that protect local-only inputs.

`gl cache` is the umbrella for cache administration.

## `gl cache status`

```bash
gl cache status
```

Prints a summary: cache root, blob count, map entry count, and pin count.

## `gl cache blobs`

Operations on the blob store directly. Mostly useful for debugging.

```text
gl cache blobs list                 # All blob hashes (one per line)
gl cache blobs check <hash>         # "exists" or "not found"
gl cache blobs path <hash>          # Filesystem path of the blob
gl cache blobs store <file>         # Add a file as a blob, print its hash
gl cache blobs get <hash>           # Print blob contents to stdout
gl cache blobs delete <hash>        # Remove a blob (use with caution)
```

Manual `delete` will break any artifact that references the blob. Prefer `gc`.

## `gl cache map`

Operations on the identity map (`identity → manifest_hash`).

```text
gl cache map list                   # All entries: "<identity> -> <manifest>"
gl cache map get <identity>         # Manifest hash for one identity
gl cache map check <identity>       # "exists" or "not found"
gl cache map set <identity> <hash>  # Set a mapping (advanced; usually unwanted)
gl cache map delete <identity>      # Force a rebuild of one artifact
```

Deleting a map entry forces the corresponding artifact to rebuild on the next `gl build`. The blobs the artifact previously produced stay in the store (they may still be referenced elsewhere); GC reclaims the orphans.

## `gl cache pin`

Pins are the GC roots for local-only input blobs. `gl import` and `gl lockfile`
/ `gl lockfile-rootfs` create them automatically — this command inspects and
removes them.

```text
gl cache pin list                   # "<id>  <n> blobs  <name>" per pin
gl cache pin show <id>              # Pin name and every blob hash it protects
gl cache pin drop <id>              # Remove a pin (its exclusive blobs become GC-eligible)
```

Dropping a pin does not delete blobs immediately; it removes their protection so
the next `gc` can reclaim any that are neither pinned by another pin nor
graph-reachable. Because pinning is many-to-many, a `.deb` shared by two lockfile
pins survives dropping just one of them. `drop` warns that if the freed blobs
were never published to a remote, they are unrecoverable after the next GC.

## `gl cache gc`

```bash
gl cache gc [--dry-run] [--conf-dir <dir>] [--arch <arch>] [--stub <path>]
```

GC keeps a blob iff it is **graph-reachable from the current checkout** or
**named by a pin**:

1. Build the conf-dir's artifact graph; for every built node keep its manifest
   blob and every leaf blob the manifest references.
2. Keep every blob named by any pin under `pins/`.

Anything in `blobs/` in neither set is deleted — it is either a stale,
rebuildable output or a re-fetchable input (`.dsc`, cached indexes). `--dry-run`
prints how many blobs would be removed without deleting. GC needs the conf-dir
to build the graph; it defaults to the discovered conf-dir and host arch, but
pass `--conf-dir` / `--arch` / `--stub` to be explicit. With no conf-dir it
protects only pinned blobs (all build outputs become collectible) and warns.
GC takes no `--cache` flag — set `GL_CACHE` to target a non-default store.

## Cache layout

```
~/.cache/gl-ng/
├── blobs/
│   ├── ab/
│   │   └── cd1234...                  # Content-addressed; first 2 chars = directory
│   └── ...
├── map/
│   ├── 12/
│   │   └── 34abcd...                  # Identity-keyed; value = manifest blob hash (text)
│   └── ...
└── pins/
    └── 01166ad6b9eb050e.yml           # One YAML file per pin (16-hex-char id): name + blob list
```

Two-character prefix sharding keeps any one directory below ~256 entries even with millions of blobs (~256 × 256 buckets = 65 K).

## Pinned input blobs

Some blobs are not produced by builds but by *imports*: orig tarballs (`gl import`)
and lockfile index blobs plus resolved `.deb`s (`gl lockfile` /
`gl lockfile-rootfs`). These are **local-only** until published to a remote, so
each import/lockfile command auto-creates a **pin** naming exactly those blobs
(see `gl cache pin`). GC keeps any pinned blob.

To reclaim space from inputs you no longer need, `gl cache pin list` to find the
pin, then `gl cache pin drop <id>`; the next `gc` collects whatever that release
left unprotected (blobs still named by another pin, or still graph-reachable,
stay). Not every import byproduct is pinned — `.dsc`/`.diff.gz` files and cached
`Sources.gz`/`InRelease` responses are re-fetchable, so they are left unpinned
and GC reclaims them normally.

## Cache layout invariants

Two invariants hold by construction:

1. **Blobs are immutable.** A blob's path is `blobs/<2>/<62>` where the 64-char hex is the SHA-256 of the file's contents. Mutating a blob would mean its path is wrong; the next read would fail content verification.
2. **Map entries can be replaced.** `map/<identity>/...` is a regular file holding a manifest hash; `gl cache map set` overwrites it. This is what `gl cache map delete` exploits to force a rebuild.

## Sharing a cache between conf-dirs

The cache is per-user, not per-conf-dir. Multiple conf-dirs pointing at the same cache directory share blobs and map entries. This is intentional — switching branches with different `build.yml`s is cheap because most blobs already exist.

## Sharing a cache between hosts

There is no first-class export/import in Phase 1. A naïve approach (`rsync`) works because the cache directory is just files; both blobs and map entries are content-addressed or hash-keyed. But `rsync`-style sharing only makes sense if the receiving host can deserialize the manifests, which requires the same `gl` binary version (or compatible).

## When to wipe

A full wipe (`rm -rf ~/.cache/gl-ng`) is sometimes the simplest fix:

- After a major schema change in the manifest format (Phase 1 still iterating).
- When investigating cache layer bugs.
- Before benchmarks where you want a cold cache.

The cost is re-fetching every orig tarball (network) and rebuilding every package (CPU-bound). On the development VM this is a few minutes; in CI, it can be 10–30 min. Plan accordingly.

## See also

- [Concept: Object Store](../concepts/object-store.md) — design rationale.
- [Inspecting Builds](./inspect.md) — using `gl cache` for build debugging.
- [Object store internals](../internals/foundations/objstore.md) — the implementation.
