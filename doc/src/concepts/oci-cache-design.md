# OCI-backed object store & pull-through cache (design draft)

> **Status:** the **local half is implemented** — the pin mechanism
> (`pins/<pin-id>.yml`), the graph-reachability-plus-pins garbage collector
> (`gl cache gc`), the `gl cache pin` CLI (list / show / drop), and the
> auto-pinning of `gl import` and `gl lockfile` / `gl lockfile-rootfs`. The old
> `Sources` type has been retired. The **remote half** (OCI registry,
> pull-through read, publish; §7–§9) is **not yet implemented** — until it
> lands, a blob GC treats as "re-fetchable by digest" is in practice re-fetched
> by re-running the import/lockfile command against the upstream repo. This page
> describes the intended direction for turning the local object store into the
> local half of a pull-through cache backed by an OCI registry, the refactor of
> garbage collection that it required, and the **pin** mechanism that protects
> locally-imported inputs from GC. It covers both **artifact outputs** (blobs
> reachable from `map` entries) and **input artifacts** (imported sources and
> lockfile dependencies).

## 1. Motivation

Today the object store is purely local (see [object-store.md](./object-store.md)).
The eventual goal is that an official/central build system publishes its build
artifacts to an OCI registry, and local builds treat their own store as a
**pull-through cache**:

- **Write** always goes to the local store (unchanged).
- **Read** on a local miss falls through to the remote; a hit there is fetched
  into the local store and returned.
- Only if the artifact is absent **both locally and remotely** does the store
  report "not available".

The registry must be a real OCI registry using [ORAS](https://oras.land/)
conventions for non-image artifacts — **not** container images with artifacts
smuggled inside layers. We use OCI's ability to store arbitrary artifact types.

## 2. The two artifact classes

Blobs in the store split into two classes with different lifetimes and
different GC roots:

| Class | Rooted by | Kept locally when | Restorable after GC via |
|---|---|---|---|
| **Output artifacts** | a `map` entry (identity → manifest) | reachable from the current checkout's build graph | rebuild |
| **Input artifacts** | a local **pin** (`pins/<pin-id>.yml`) | covered by a local **pin** | remote pull-through (if published) |

The unifying GC principle (§5) is: **a blob is kept iff it is graph-reachable
from the current checkout, or it is named by a local pin. Everything else is
free to delete**, because it is either remote-mirrored (re-fetchable by digest
via pull-through) or a stale build output (rebuildable). GC never asks "is this
blob still *used*" — only "reachable-or-pinned, yes/no".

## 3. What the current output model already gives us

Established facts about the present code (no change required to rely on them):

- **Identity is a pure function of `(own config + dependency identities)`.** It
  never reads build outputs. So the identity of every artifact — and thus the
  complete set of `map` keys the current conf-dir would produce — can be
  computed **without building anything**
  (`internal/build/{source,binary,rootfs}.go` `Identity()`).
- **The build graph already enumerates the active artifact set**, with dedup
  and cycle detection, from the top-level target(s) via
  `artifact.Discover(roots)` (`internal/artifact/graph.go`). `Discover` takes a
  slice of roots, so the multi-rootfs future needs no traversal change.
- **A build produces multiple outputs, each an independent leaf blob** with zero
  further indirection (`Build() []Output`, each `Output{Name, Hash}` an
  already-stored leaf).
- **The manifest is the only blob that references other blobs**, and it is
  created by the *engine*, not by `Build()`, uniformly for every artifact —
  even single-output ones like `Rootfs`. The map always points at this manifest
  (`internal/artifact/manifest.go`). The chain is uniformly depth-3:

  ```
  map[identity] → manifest blob → { leaf, leaf, … }
  ```

- **Leaf blobs are shared across map entries** (fan-in): `debianBinaryPkg`
  re-emits its parent source build's `.deb`/`control:` leaves, so two distinct
  manifests list overlapping leaves. GC must be reachability/refcount based,
  never ownership based.

The one thing the current code does **not** expose is a structural way to go
from a built `map` entry to its blob closure — the references live only inside
the manifest blob's opaque `"<hash> <name>"` text.

## 4. New interface surface: `OutputRefs`

Add one method to the `Artifact` interface
(`internal/artifact/artifact.go`) that exposes the already-built output closure.
It does **not** build:

```go
// OutputRefs returns the blob references for an artifact that has already
// been built. manifest is the hash of the map's target blob (the local
// manifest / entrypoint); leaves are the output blobs it references.
// Returns an error if the artifact has not been built (no map entry).
OutputRefs(store *objstore.Store) (manifest objstore.Hash, leaves []objstore.Hash, err error)
```

Notes:

- **Two return values, deliberately.** `manifest` is the entrypoint the `map`
  points at; `leaves` are the outputs. Callers need the split: GC must keep
  both, and the uploader treats the manifest as a local-only entrypoint (see
  §6) while the leaves become OCI layers.
- **Not built → error.** Callers that walk the whole graph (GC, upload)
  `try OutputRefs, continue on error` — an unbuilt or partially-built artifact
  simply contributes nothing.
- **Implementation is identical for every artifact type** (Identity → `Map.Get`
  → read manifest blob → parse `"<hash> <name>"` lines → return manifest hash +
  leaf hashes). Provide it via a shared helper so each type's method is a
  one-liner; the existing `Engine.loadManifest`
  (`internal/artifact/manifest.go:36`) is the parsing logic to factor out.
- This method makes no reference to OCI. It is the single source of truth for
  "what blobs does this built artifact consist of", consumed by both local GC
  (§5) and the uploader (§6).

## 5. Garbage collection moves to the build layer

Today GC lives in `cmd/gl/cache.go:cacheGC` and operates directly on the store:
it marks every `map` **value** reachable and deletes the rest. This is **broken
today** — it is non-recursive, so it would delete every `.deb`, `control:`, and
`rootfs.tar.gz` leaf (they are reachable only *through* the manifest's text).
It also has no knowledge of which map entries belong to the *current* conf-dir.

New split:

- **objstore provides only mechanics, no policy.** Add a dumb sweep primitive:

  ```go
  // Sweep deletes every blob not in keep. It applies no policy of its own.
  func (b *BlobStore) Sweep(keep map[objstore.Hash]struct{}) (deleted int, err error)
  ```

  The store still owns pull-through semantics (§6) and the low-level delete; it
  does **not** decide what to keep.

- **The build system computes the keep-set.** GC becomes:

  1. Assemble the current build graph (`build.BuildGraph` → `Discover(roots)`).
  2. For every node, call `OutputRefs(store)`; on error (not built), skip it.
  3. Union all returned `manifest` hashes **and** all `leaves` into the
     keep-set.
  4. Union every blob named by any **local pin** (§6) into the keep-set.
  5. Call `store.Blobs.Sweep(keep)`.

  This keeps exactly (a) the blobs the current checkout can still produce, at
  one level of manifest indirection resolved structurally rather than by parsing
  text out of band, and (b) the blobs a user has locally imported/lockfiled and
  not yet pushed. Fan-in leaves are naturally retained because they appear in
  multiple artifacts' `OutputRefs`.

**What GC deliberately does *not* protect, and why that is safe:**

- **`sources.yml` / `*-deps.yml`-referenced blobs are *not* walked.** GC does
  not read these working-tree files and does not follow the lockfile index
  blob's `SHA256:` fields. The git repo already records the orig-tarball hashes
  (`sources.yml`) and the lockfile index hash (`*-deps.yml`), and the index in
  turn records the `.deb` hashes. So any such blob that came from the remote is
  re-fetched on demand by digest through the pull-through store (§7) after being
  swept. Only blobs that are **local-only** (never published to the remote) need
  protection, and those get it via a pin — not via graph or working-tree
  traversal.
- **No git-history traversal, ever.** Protection must not depend on which commit
  is checked out. Reachability is computed from the *current* checkout only;
  durable local-only inputs are protected by pins, which live in the object
  store and are independent of git state. A blob referenced only by some other
  branch's `*-deps.yml` is *not* protected — if it is remote-mirrored it
  re-fetches, and if it is local-only the user is expected to have pinned it.

## 6. Pins: protecting local-only inputs

A **pin** is the only mechanism that protects input-artifact blobs from GC. It
exists solely for blobs that are **local-only** — imported or lockfiled by the
user and not (yet) available from the remote. Remote-mirrored inputs need no
pin: they are re-fetchable by digest.

**On-disk form.** Pins live in the object store next to `blobs/` and `map/`,
**not** in the git repo:

```
<root>/
├── blobs/
├── map/
└── pins/
    └── <pin-id>.yml
```

Each pin file is small YAML:

```yaml
name: "libfoo 1.2.3 orig (bookworm, 2026-03-01T00:00:00Z from deb.debian.org)"
blobs:
  - <sha256>
  - <sha256>
```

- **`<pin-id>`** is a randomly-generated **64-bit** value rendered as 16
  lowercase hex chars (e.g. `9f3a1c07be42d5a1`). A full UUID is unnecessary;
  64 random bits is ample for collision-free local pin identity, and the same
  id is reused as the remote pin tag suffix (§7b) so a local↔remote match is a
  trivial string equality.
- **`name`** is free-form human text describing the pin's origin (package,
  version, suite, source repo, and the **`InRelease` timestamp** — never wall
  clock — so time-travel / reproducibility scenarios stay coherent).
- **`blobs`** is a flat list of the blob hashes this pin protects.

**Why per-id files rather than a flat blob→label map.** A single blob can belong
to several pins — e.g. a `.deb` pulled in by two different lockfile imports, or
later also provided by a remote publish. A per-pin file with a blob *list* makes
this many-to-many trivially: the same hash simply appears in multiple pin files.
GC keeps a blob if **any** pin lists it. Dropping a pin is `rm pins/<pin-id>.yml`;
the blob survives as long as another pin (or the graph) still references it.

**Granularity: source and build-deps are separate pins.** `gl import` creates a
**source pin** (the orig tarballs). `gl lockfile` / `gl lockfile-rootfs` create
a **per-arch build-deps pin** (the lockfile index blob plus every `.deb` it
references). This matches the input lifecycle: once an artifact is built, its
build-time deps are no longer needed to keep *using* the output, so a user can
drop heavy per-arch build-deps pins while keeping the source pin — a deliberate
manual choice, because no automatic check can prove another branch won't want
them again.

**Auto-pin is load-bearing.** The commands that store local-only input blobs
must create the corresponding pin as part of the same operation — pin creation
must complete before the command reports success. If a blob were stored without
a pin, a GC running between the import and the first remote push would delete
local-only work permanently (there is no remote copy to re-fetch). This
replaces the current `Sources.Mark` mechanism entirely: the `sources` file and
the `Sources` type go away; orig tarballs become a source pin instead.

**Dropping a pin is irreversible for local-only blobs.** If a pinned blob was
never published to the remote, dropping its pin and running GC makes it
unrecoverable. That is intended: the pin *is* the durability guarantee, and
releasing it is an explicit user act. Once the same import has been published
to the remote (§8, same pin-id), the local pin is redundant and safe to
drop — pull-through will restore the blobs by digest.

## 7. OCI mapping (ORAS conventions)

Two things get published, with two layouts: **output artifacts** (map entries)
and **input pins** (source / build-deps imports).

### 7a. Output artifacts

The local manifest format is an **internal detail** and is **not uploaded**.
What must be preserved across local and remote is the mapping
`identity → set of output blobs`, not the byte-identity of the manifest file.
So local and remote each maintain their own manifest representation and convert
between them.

**Blobs → OCI blobs, 1:1.** Leaf blobs are pushed to
`/v2/<name>/blobs/<sha256>`; content-addressing lines up exactly.

**Map entry → tag → ORAS manifest.** For each map entry the uploader:

1. Calls `OutputRefs` to get `(manifest, leaves)`.
2. Pushes each leaf blob.
3. Constructs an **ORAS-style OCI image manifest**
   (`application/vnd.oci.image.manifest.v1+json`) with a project-specific
   `artifactType`, an empty config
   (`application/vnd.oci.empty.v1+json`), and one `layers[]` descriptor per leaf
   blob (digest + size + an `org.opencontainers.image.title`-style annotation
   carrying the output `Name`).
4. **Tags** that manifest with the artifact identity. The tag is the only GC
   root on the registry; because a tagged manifest structurally references its
   layers, registry GC keeps the leaves alive automatically — no bare-blob
   reliance.

Because the leaf→output-name mapping is carried in layer annotations, the
remote manifest fully reconstructs the local manifest. The local manifest is
never uploaded.

**Pull-through read.** When a `map` lookup misses locally:

1. Fetch the tag (= identity) from the remote → get the ORAS manifest.
2. Fetch each layer blob into the local blob store.
3. **Convert** the ORAS manifest back into the local
   `"<hash> <name>"` manifest, store it, and set the `map` entry to point at it.
4. Return as a normal local hit.

Only if the tag is absent remotely too does the lookup report "not available".

**Why no intermediate index is needed (yet).** A tag may resolve directly to a
single image manifest; the OCI *index*
(`application/vnd.oci.image.index.v1+json`) is optional and only needed to group
several manifests under one tag (e.g. multi-arch). Output artifacts are
per-arch identities today, so one tag → one manifest suffices. An index becomes
relevant if we later want a single logical tag to fan out over arch variants.

### 7b. Input pins

Pins publish with the **same identity and the same source-vs-build-deps split**
as their local counterparts (§6), so a local pin and its remote publication are
the same conceptual object under the same pin-id — even though the local pin
file and the remote OCI manifest are independent representations that merely
agree.

- **Tag = `import/<pin-id>`** (or `builddeps/<pin-id>`), the pin's 64-bit hex
  id — the same id the local pin file uses. This makes "has my local pin
  migrated to remote" a trivial tag-existence check, and migrating a pin
  local→remote a matter of pushing under the already-chosen id.
- The tag points at an **ORAS manifest** carrying:
  - the pin's human **`name`** (package, version, suite, source repo, and the
    `InRelease` timestamp) as an annotation / config — the same text as the
    local pin's `name`;
  - one `layers[]` descriptor per blob the pin protects.
- **The build system's multi-level structure is flattened here.** A build-deps
  pin's manifest places the **lockfile index blob side by side with every `.deb`
  it references** as sibling layers — the index-references-`.deb` relationship
  that is semantic on the build side becomes a flat layer list on the registry.
  The manifest exists mainly so the registry does not GC these blobs (the tag
  roots them); unlike output manifests it carries no build-system meaning that
  needs converting back — the git repo's `sources.yml` / `*-deps.yml` hashes
  remain the authority for what references what.

**Local side does not mirror input pins.** Publishing a pin makes its blobs
remotely re-fetchable by digest, at which point the local pin is redundant (§6).
The local store never needs to know the remote pin tags: pull-through fetches
input blobs **by digest**, driven by the hashes already in the git repo, not by
tag. Input-pin tags exist purely to keep the registry from collecting the blobs.

## 8. Edge cases and constraints

- **Fan-in leaves:** keep-set is a union across all artifacts; a leaf shared by
  a source build and its binary packages is kept as long as *any* referencing
  artifact is live. OCI blob sharing by digest handles the remote side.
- **A blob shared by an output and a pin** (e.g. a `.deb` that is both a build
  output and a build-deps input) is kept if *either* roots it. Because the
  keep-set is a plain union of graph reachability and pin membership, this needs
  no special handling — the many-to-many pin model (§6) already covers a blob
  belonging to multiple pins and/or the graph simultaneously.
- **The InRelease cookie-cache map entry and the downloaded APT package/source
  indices** (`internal/debian/aptrepo/aptrepo.go`, and the re-used downloaded
  `Packages.gz`/`Sources.gz`, *not* the `*-deps.yml`-referenced lockfile
  indices) are **genuine caches**. There is never a reason to protect them from
  GC: a miss simply re-downloads. They need no keep-set entry — they fall
  outside the "must protect" set entirely and are collected freely. This is a
  feature, not an edge case.
- **Partially-built graphs:** `OutputRefs` returning an error for unbuilt nodes
  is the intended signal; GC and upload both skip on error, so a half-built
  store neither over-deletes nor fails.
- **Registry GC timing** is registry-specific and not real-time; the pull-through
  contract only relies on *tagged* manifests being retained, which is universal.

## 9. Explicitly deferred

- Multi-target / multi-arch rootfs and whether an OCI index groups arch variants
  under one tag (§7a notes where an index would slot in).
- Remote authentication, push permissions, and which identities the central
  builder publishes vs. which a local build may push.
- The migration/cutover from the current `Sources` mechanism to pins (§6 states
  the end state; the transition is unspecified).
- `gl` command surface for pins — listing pins, showing what a pin protects,
  and the manual drop. **No such command exists today** (`gl cache` currently
  offers only `gc` / `status` / `map` / `blobs`, `cmd/gl/cache.go`); the pin CLI
  has to be designed from scratch. The local action plan (§10) proposes a
  minimal set.

---

## 10. Action plan — local side only

This section is the concrete plan for a future implementing agent. Its scope is
**strictly the local store**: the pin data type, the pin/GC refactor, and wiring
the import/lockfile commands to auto-pin. It explicitly does **not** cover the
remote registry, pull-through fetch, or publish — those build on top of this and
come next. Follow the [doc-and-tests rule](../guidelines/doc-and-tests-rule.md):
each functional step below names its test obligation.

Read the current code before starting: `internal/objstore/` (`store.go`,
`blobs.go`, `mapstore.go`, `sources.go`), `internal/artifact/`
(`artifact.go`, `manifest.go`, `engine.go`, `graph.go`), `internal/importer/`
(`import.go`, `sources_yml.go`), `internal/lockfile/generate.go`,
`cmd/gl/cache.go`. Verify every symbol referenced below still exists.

### Step 1 — `OutputRefs` on the artifact model

- Add to the `Artifact` interface (`internal/artifact/artifact.go`) a method
  that returns the built output closure without building:
  `OutputRefs(store *objstore.Store) (manifest objstore.Hash, leaves []objstore.Hash, err error)`.
- Factor the existing manifest-parsing logic out of `Engine.loadManifest`
  (`internal/artifact/manifest.go`) into a shared helper: given an identity, do
  `Identity()` → `Map.Get` → open the manifest blob → parse `"<hash> <name>"`
  lines. Return the manifest blob hash and the leaf hashes.
- Return an error (do not panic, do not return empty-success) when the artifact
  has no map entry, so callers can `try / skip`.
- Implement the method on all artifact types (`DebianPkgBuild`,
  `debianBinaryPkg`, `Rootfs`) — each delegates to the shared helper, so each
  is a one-liner.
- **Tests (mandatory):** a built artifact returns its manifest hash + exactly
  its leaf hashes; an unbuilt artifact returns an error; a `Rootfs` (single
  output) and a multi-binary `DebianPkgBuild` (fan-out) both round-trip
  correctly. This pins identity→outputs resolution, which can fail silently.

### Step 2 — the pin store (`internal/objstore`)

- New file `internal/objstore/pins.go` defining a `Pins` type, constructed in
  `Open()` (`store.go`) alongside `Blobs`/`Map`/`Sources`, backed by a
  `pins/` subdirectory under the store root.
- On-disk: one file per pin, `pins/<pin-id>.yml`, where `<pin-id>` is 16
  lowercase hex chars (64 random bits from `crypto/rand`). File schema:
  a `name:` string and a `blobs:` list of 64-hex blob hashes (see §6).
- API (names illustrative, shape fixed):
  - `Create(name string, blobs []Hash) (id string, err error)` — generate a
    fresh id, write the file atomically (temp + rename, matching `sources.go`'s
    `save()` pattern), return the id.
  - `List() []Pin` — read every `pins/*.yml`.
  - `Get(id string) (Pin, error)`.
  - `Drop(id string) error` — delete the file.
  - `ReachableBlobs() map[Hash]struct{}` — union of every pin's `blobs`, for GC.
- Ignore/skip malformed pin files on read (log, don't fail the whole store),
  mirroring `Sources.load()`'s tolerance.
- **Tests (mandatory):** create→list→get round-trip; ids are 16-hex and unique
  across many creates; a blob in two pins appears once in `ReachableBlobs`;
  drop removes only the named pin and leaves shared blobs listed by other pins;
  malformed file is skipped, not fatal.

### Step 3 — auto-pin the importer, retire `Sources`

- `gl import` (`internal/importer/import.go`): after storing the orig-tarball
  blobs, create **one source pin** listing exactly those blob hashes, with a
  `name` of the form `"<pkg> <version> orig (<suite>, <InRelease-timestamp> from <repo>)"`.
  The timestamp is the `InRelease` date, never wall clock.
- Replace the current `Sources.Mark` calls (`import.go` two sites) with the pin
  creation. Remove `internal/objstore/sources.go` and the `Sources` field from
  the store, and drop the `sources` file from the on-disk layout. Update any
  reader (`cache.go` status/gc) accordingly.
- Pin creation must complete before `gl import` reports success (auto-pin is
  load-bearing — §6). If pin write fails, the import fails.
- **Tests (mandatory):** after an import, the orig blobs are listed by exactly
  one pin; the pin `name` carries the InRelease timestamp; no `sources` file is
  created. Migrate/replace the existing `Sources` tests.

### Step 4 — auto-pin the lockfile commands

- `gl lockfile` and `gl lockfile-rootfs` (`internal/lockfile/generate.go`,
  `Generate` / `GenerateRootfs`): after `runResolveFetchAndStore` stores the
  lockfile index blob and its `.deb` blobs, create **one per-arch build-deps
  pin** listing the index blob hash **and** every `.deb` hash it references,
  with a `name` identifying package/rootfs, arch, suite, and InRelease
  timestamp.
- One pin per invocation (per arch), consistent with the split granularity
  (§6). Re-running for another arch creates a second pin.
- Pin creation completes before the command reports success.
- **Tests (mandatory):** after `gl lockfile`, the index blob and all its `.deb`
  hashes are covered by one build-deps pin; a second arch produces a distinct
  pin; the `.deb` shared between two arch pins appears in both and survives
  dropping one.

### Step 5 — rewrite GC (`cmd/gl/cache.go`) and add `Sweep`

- Add a policy-free primitive `BlobStore.Sweep(keep map[Hash]struct{}) (int, error)`
  in `internal/objstore/blobs.go` that deletes every blob not in `keep`. It
  decides nothing.
- Rewrite `cacheGC` so it no longer marks map values directly. Instead:
  1. Build the current graph (`build.BuildGraph` → `artifact.Discover(roots)`).
  2. For each node call `OutputRefs`; on error, skip. Union manifest hash + leaf
     hashes into the keep-set.
  3. Union `store.Pins.ReachableBlobs()` into the keep-set.
  4. `store.Blobs.Sweep(keep)`.
- Keep `--dry-run` (count only). Do **not** protect `sources.yml`/`*-deps.yml`
  blobs by any other means — the pin union is the sole input-protection path,
  and unpinned remote-mirrored blobs are intentionally collectible (§5).
- This also fixes the current correctness bug where GC deletes output leaves
  (it was non-recursive; now the closure comes structurally from `OutputRefs`).
- **Tests (mandatory):** GC keeps a built rootfs's full leaf closure; GC keeps
  pinned import blobs; GC deletes an unpinned, unreachable blob; GC deletes a
  stale output whose artifact is no longer in the graph; `--dry-run` deletes
  nothing. This is the highest-risk change — over-deletion is data loss.

### Step 6 — `gl` pin CLI (minimal)

- Add pin subcommands (either `gl pin ...` or `gl cache pin ...` — pick one and
  document it). Minimum:
  - `list` — id, name, blob count per pin.
  - `show <id>` — the pin's blobs.
  - `drop <id>` — delete the pin (the manual cleanup path from §6). Warn that
    dropping a pin for local-only, unpublished blobs is irreversible after GC.
- **Tests:** command-level tests for list/show/drop against a seeded store.

### Ordering and checkpoints

Steps 1–2 are independent and can land first. Step 5 depends on 1 and 2. Steps
3–4 depend on 2 and should land before or with 5 (otherwise the first GC after
the `Sources` removal would delete imports). Step 6 depends on 2. After step 5,
the local store is complete for the pull-through/publish work to build on.

### Docs to update as part of this work

- `doc/src/concepts/object-store.md` and
  `doc/src/internals/foundations/objstore.md` — add `pins/`, remove `sources`.
- `doc/src/guide/cache.md` — GC behavior and the new pin commands.
- `doc/src/concepts/identity.md` — its description of GC roots is currently
  aspirational and wrong; align it with the graph+pin model.
- This page's status note, once implementation begins.
