# The Object Store

The object store is the only persistent state in the entire build system. Build outputs, source tarballs, lockfile blobs, manifests — everything that survives a process — lives here. It is intentionally minimal: two directories of content plus a directory of pins.

## On-disk layout

By default, the store lives at `~/.cache/gl-ng/` (overridable via the `GL_CACHE` environment variable or the `--cache` CLI flag).

```
<root>/
├── blobs/
│   ├── 0e/
│   │   ├── 8c40f9b...c34a    ← content-addressed file
│   │   └── b2a3...
│   ├── 9f/
│   │   └── ...
│   └── ...
├── map/
│   ├── 4d/
│   │   ├── 7728...3e0e       ← tiny file containing a blob hash
│   │   └── ...
│   └── ...
└── pins/
    ├── 01166ad6b9eb050e.yml  ← one YAML file per pin (16-hex-char id)
    └── ...
```

### `blobs/` — content-addressed storage

Every entry is sharded by the first two hex characters of its SHA-256 hash; the filename is the remaining 62 characters. So a blob with hash `0e8c40f9...c34a` lives at `blobs/0e/8c40f9...c34a`.

Properties:

- **Immutable**: a blob's content cannot change. If you have its hash, you have its content.
- **Atomic write**: storage is `tempfile + rename`, with the SHA-256 computed during the copy via `io.TeeReader`.
- **Idempotent**: storing a blob that already exists is a no-op.
- **No directory listing required at runtime**: the path is a pure function of the hash.

### `map/` — identity → blob mappings

Every entry is keyed by an *identity hash* (computed from artifact inputs) and contains a single line: the hex hash of a target blob. The same sharding rule applies: identity `4d7728...3e0e` lives at `map/4d/7728...3e0e`.

Map values are typically *manifest blobs*. A manifest is a small text blob with one line per output:

```
<blob-hash> <output-name>
<blob-hash> <output-name>
```

So the chain for "look up artifact identity `id`" is:

1. Read `map/<id>` → get manifest hash `m`.
2. Read `blobs/<m>` → get manifest text.
3. Parse: a list of `(blob hash, output name)` tuples.

Entries in `map/` are typically immutable once set, but the API allows updates (it is a regular file rename, atomic per-entry).

### `pins/` — GC roots for local-only inputs

One YAML file per pin, named `pins/<pin-id>.yml`, where `<pin-id>` is a random
16-hex-character (64-bit) id:

```yaml
name: "glibc 2.43-6 orig (testing, Mon, 28 Sep 2026 08:06:21 UTC from https://deb.debian.org/debian)"
blobs:
  - 0b89b8c058eaa0...c34a
  - 73f5a2c1d9...11de
```

A pin names a set of blobs that garbage collection must not evict. Pins protect
blobs that are **local-only** — imported orig tarballs, lockfile index blobs,
and the `.deb` files a lockfile resolved — for which no `map` entry describes
how to rebuild them. `gl import` creates one **source pin** per package (the
orig tarballs); `gl lockfile` / `gl lockfile-rootfs` create one **build-deps
pin** per package per architecture (the lockfile index blob plus every resolved
`.deb`). Pinning is many-to-many: a `.deb` shared by two lockfiles is named by
both pins and survives until both are dropped. See
[oci-cache-design.md](./oci-cache-design.md) for the full model.

By contrast, build output blobs are regenerable: their inputs are in the source
tree and lockfiles, so they can always be rebuilt, and they are kept by graph
reachability rather than by a pin.

## What goes in the store

| Blob class | Source | Kept by |
|------------|--------|-----------|
| Orig tarballs | `gl import` | source pin |
| `.dsc`, `.diff.gz`, etc. (Debian source files) | `gl import` | nothing — re-fetchable, GC reclaims |
| Lockfile package indexes | `gl lockfile` (or `gl lockfile-rootfs`) | build-deps pin |
| Build-deps `.deb` files | Fetched at lockfile-time from Debian mirror | build-deps pin |
| Built `.deb` files | `DebianPkgBuild.Build()` outputs | graph reachability (rebuildable) |
| Output rootfs `.tar.gz` | `Rootfs.Build()` output | graph reachability |
| Manifest blobs | Engine's output serialization | graph reachability |
| Cached HTTP responses (e.g. `InRelease`, `Sources.gz`) | `gl import`, `gl lockfile` | nothing — re-fetchable, GC reclaims |

Note that `.deb` files in the lockfile are *fetched* (not built), but they are
pinned anyway: they are local-only until published to a remote, so a GC between
lockfile generation and the first remote push would lose them permanently. By
contrast the `.dsc`/`.diff.gz` and cached HTTP indexes are re-fetchable by
re-running `gl import` / `gl lockfile` against the same upstream state, so they
are left unpinned and GC reclaims them.

## Cookies and HTTP caching

Some operations need to fetch large indexes from a remote (like `https://deb.debian.org/debian/dists/testing/InRelease`). To avoid hitting the network repeatedly within a single batch operation, fetches can be keyed by a *cookie* — an arbitrary user-supplied string that participates in the cache key.

The cache key is computed as:

```
identity = ConcatHash("inrelease-cookie", cookie, repoURL, dist)
map[identity] = blob hash of the cached response
```

When `prepare_staging.sh` generates a UUID cookie at the top and threads it through every `gl import` and `gl lockfile` call, all those invocations share one InRelease fetch. Without a cookie, the fetch always re-runs.

## Concurrency

The blob store is safe for concurrent writes (atomic rename). Each pin is a separate file under `pins/`, created with a temp-file + atomic-rename write, so pin creation is likewise concurrency-safe — there is no single shared index file to serialize on. Pin ids are drawn from `crypto/rand`, so two concurrent importers do not collide.

## Validation

When `Map.Set(key, value, validate=true)` is called, the store verifies that `value` references an existing blob. The `validate=false` mode exists for bootstrapping scenarios where the blob will be created shortly afterwards. In practice, the engine always uses `validate=true` after calling `Blobs.Store`.

## Garbage collection

`gl cache gc` computes the keep-set and deletes everything else. The algorithm:

1. **Graph reachability.** Build the current checkout's artifact graph. For
   every node that has been built (has a `map` entry), keep its manifest blob
   and every leaf blob the manifest references.
2. **Pins.** Keep every blob named by any pin under `pins/`.
3. Delete every blob in `blobs/` that is in neither set.

A blob is kept iff it is graph-reachable from the current checkout **or** named
by a pin. Everything else is either a stale, rebuildable output or a
re-fetchable input (`.dsc`, cached indexes), so deleting it is safe. `--dry-run`
reports how many blobs would be removed without deleting. GC needs the conf-dir
(and arch/stub) to build the graph — see [the cache guide](../guide/cache.md).

This is the local half of the [OCI-cache design](./oci-cache-design.md). The
remote pull-through that lets a GC'd input be re-fetched by digest is now
implemented (set `GL_REGISTRY`); when no registry is configured, a reclaimed
input is still restored by re-running the import/lockfile command.

## Why this design

The whole store fits in a few hundred lines of Go (see [`internal/objstore`](../internals/foundations/objstore.md)). A more elaborate design — SQLite, embedded KV store, S3 wrapper — would buy nothing. Content-addressed file trees are:

- **Mountable as a remote**: the remote object-store interface (`Store.Remote`,
  implemented by [`internal/ociclient`](../internals/foundations/ociclient.md)
  over an OCI registry) exposes the same hash-based GET. Cache misses fall
  through to the remote and are materialized locally; hits stay local.
- **Diff-friendly**: `rsync` between two stores is correct as long as the receiver is up-to-date with the sender's `pins/` directory.
- **Inspectable**: `ls blobs/<2char>/` lists the blobs in a shard; `cat map/<2char>/<rest>` shows what an identity points to.

The API surface is correspondingly small. See [the objstore internals page](../internals/foundations/objstore.md) for the exact Go signatures.
