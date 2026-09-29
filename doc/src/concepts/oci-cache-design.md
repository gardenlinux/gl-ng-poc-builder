# OCI-backed object store & pull-through cache (design draft)

> **Status:** **both halves are implemented.** The **local half** — the pin
> mechanism (`pins/<pin-id>.yml`), the graph-reachability-plus-pins garbage
> collector (`gl cache gc`), the `gl cache pin` CLI (list / show / drop), and
> the auto-pinning of `gl import` and `gl lockfile` / `gl lockfile-rootfs`. The
> old `Sources` type has been retired. The **remote half** (OCI registry
> client, pull-through read hook, `gl cache push`, `GL_REGISTRY` wiring; §11–§13)
> now lands as `internal/ociclient`, `Store.OpenBlob` / `Store.MapGet` /
> `Store.EnsureBlob`, and `gl cache push`. A blob the GC treats as "re-fetchable
> by digest" is now actually pulled from the configured registry on demand (and,
> failing that, still re-fetchable by re-running the import/lockfile command
> against the upstream repo). This page
> describes turning the local object store into the
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

---

## 11. Remote registry — concrete on-registry layout

§7 fixed the shape (ORAS manifests, tags as roots); this section nails down the
**exact bytes** a future implementer must produce, grounded in the code as it
stands today (verified 2026-09-28 against `internal/objstore`,
`internal/artifact`). Everything here is registry-side wire format; §12 covers
the Go that emits and consumes it.

### 11.0 Facts the layout builds on

- Every address in the store — blob content hash, `map` key (artifact
  identity), `map` value (manifest hash) — is the **same `objstore.Hash`**: a
  validated 64-lowercase-hex SHA-256 (`internal/objstore/hash.go:26`,
  `NewHash` rejects anything not `^[0-9a-f]{64}$`). This is exactly OCI's
  `sha256:<64hex>` digest minus the `sha256:` prefix, so blob↔OCI-blob and
  identity↔tag conversions are pure string surgery, no re-hashing.
- A built artifact's output closure is already exposed structurally by
  `artifact.ResolveOutputRefs(a, store) (manifest objstore.Hash, leaves []objstore.Hash, err error)`
  (`internal/artifact/manifest.go:71`) — this is the single source of truth the
  uploader consumes. It errors on unbuilt artifacts; the uploader skips those.
- A pin is `Pin{ ID string /*16-hex*/, Name string, Blobs []Hash }`
  (`internal/objstore/pins.go:25`); `Pins.List()` and `Pins.ReachableBlobs()`
  give the uploader its input.
- There is **no OCI/registry/HTTP-client dependency in `go.mod`** today (only
  `golang.org/x/{sys,term}` and `yaml.v3`). The registry client is built on
  stdlib `net/http` (§12.1) — the codebase already uses `net/http` directly for
  APT fetching, so this matches house style; no ORAS Go SDK is pulled in.

### 11.1 Registry namespace (the OCI `<name>`)

All objects for one logical gl-ng store live under a single configurable OCI
repository name, e.g. `gl-ng` (GHCR: `ghcr.io/<org>/gl-ng`; local test:
`localhost:5000/gl-ng`). Blobs are shared across all artifact/pin manifests in
that one repository — critical, because fan-in leaves (§8) and a `.deb` that is
both an output and a build-deps input must be **one** registry blob addressed by
digest, never duplicated per tag. Do **not** split outputs and pins into
separate repositories; that would break blob sharing across the two classes.

The repository name is the *only* deployment knob that varies between local
test and GHCR. Tag structure, media types, and annotations below are identical
everywhere.

### 11.2 Blobs: 1:1, by digest

Each local blob `h` (an `objstore.Hash`) maps to the OCI blob
`sha256:<h>`, pushed to / fetched from `/v2/<name>/blobs/sha256:<h>` via the
standard chunked/monolithic upload dance (`POST` → `PUT ?digest=`) and
`GET`. Content-addressing lines up exactly; a blob already present
(`HEAD /v2/<name>/blobs/sha256:<h>` → 200) is not re-uploaded. This holds
uniformly for leaf blobs, manifest-referenced `.deb`s, lockfile index blobs, and
orig tarballs — the store does not care what a blob *is*.

The local **manifest blob** (the `"<hash> <name>\n"` text,
`internal/artifact/manifest.go:13`) is **never pushed as an OCI blob** — it is a
local-only entrypoint reconstructed from the OCI manifest's layer annotations on
pull (§11.4). This is the one asymmetry: the identity→outputs *mapping* is
preserved, the manifest *bytes* are not.

### 11.3 Tags — the two namespaces

Tags are the only GC roots on the registry, and a tag reference must satisfy
the OCI grammar `[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}`. Our identifiers do:

| Purpose | Tag | Points at |
|---|---|---|
| **Output artifact** | `build-artifact-<identity>` | image manifest, §11.4 |
| **Source import pin** | `import-<pin-id>` | image manifest, §11.5 |
| **Build-deps pin** | `builddeps-<pin-id>` | image manifest, §11.5 |

- `<identity>` is the 64-hex artifact identity verbatim (`Identity().String()`).
  `build-artifact-` + 64 hex = 78 chars, within the 128 limit. The `/`-style
  hierarchical tag floated in §7 (`build_artifacts/<sha>`) is **rejected**: a
  slash is not legal in an OCI tag (it is the repo-name separator). We encode
  the "namespace" as a tag *prefix* instead — same grouping intent, valid
  grammar. Listing all outputs is `GET /v2/<name>/tags/list` filtered by the
  `build-artifact-` prefix.
- `<pin-id>` is the pin's existing 16-hex id (`pins.go`), reused verbatim so a
  local-pin ↔ remote-publication match is trivial string equality (§7b). The
  source/build-deps split is carried in the prefix, mirroring the local pin
  granularity (§6).

Tag prefixes are a documented constant set; a future arch-index (§9) would add
an `index-` family without disturbing these.

### 11.4 Output-artifact manifest (per `build-artifact-<identity>` tag)

An OCI **image manifest** (`application/vnd.oci.image.manifest.v1+json`),
ORAS-style (config is the empty descriptor, artifact identity carried in
`artifactType` + annotations):

```json
{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.manifest.v1+json",
  "artifactType": "application/vnd.gl-ng.artifact.v1",
  "config": {
    "mediaType": "application/vnd.oci.empty.v1+json",
    "digest": "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
    "size": 2,
    "data": "e30="
  },
  "layers": [
    {
      "mediaType": "application/vnd.gl-ng.output.v1",
      "digest": "sha256:<leaf-hash>",
      "size": <bytes>,
      "annotations": { "org.opencontainers.image.title": "<output-name>" }
    }
  ],
  "annotations": {
    "vnd.gl-ng.identity": "<identity-hex>"
  }
}
```

- **Empty config** is the OCI-standard sentinel: media type
  `application/vnd.oci.empty.v1+json`, digest of the two bytes `{}`
  (`sha256:44136fa3…ff8a`), size 2, inline `data: "e30="`. The two `{}` bytes
  must still be pushed as a blob (registries require the config blob to exist);
  push it once, it is shared by every manifest.
- **One layer per leaf**, in the deterministic order `ResolveOutputRefs`
  returns them (which is manifest-file order — stable). Each layer's
  `org.opencontainers.image.title` annotation carries the **output `Name`**
  (the second field of the local manifest line). This is what lets pull-through
  reconstruct the local `"<hash> <name>"` manifest exactly (§11.6) without ever
  transporting the manifest blob.
- `artifactType` `application/vnd.gl-ng.artifact.v1` marks the manifest as ours;
  the `vnd.gl-ng.identity` top-level annotation redundantly carries the identity
  (the tag already encodes it, but the annotation survives if a tool strips the
  tag). Media type `application/vnd.gl-ng.output.v1` on layers is cosmetic —
  content is opaque bytes — but keeps `crane manifest` output self-describing.
- The manifest is `PUT /v2/<name>/manifests/build-artifact-<identity>` with
  `Content-Type` = the manifest media type. Tagging *is* the PUT — the registry
  roots the layers off the tagged manifest, so registry GC keeps the leaves with
  no bare-blob reliance (§7a).

### 11.5 Input-pin manifest (per `import-<id>` / `builddeps-<id>` tag)

Same image-manifest skeleton, different `artifactType` and annotation set, and
the pin's blobs flattened into sibling layers (§7b):

```json
{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.manifest.v1+json",
  "artifactType": "application/vnd.gl-ng.pin.v1",
  "config": { "mediaType": "application/vnd.oci.empty.v1+json", "...": "…" },
  "layers": [
    { "mediaType": "application/vnd.gl-ng.pin-blob.v1", "digest": "sha256:<blob>", "size": <n> }
  ],
  "annotations": {
    "vnd.gl-ng.pin.id":   "<16-hex>",
    "vnd.gl-ng.pin.name": "<pin human name incl. InRelease timestamp>",
    "vnd.gl-ng.pin.kind": "import" | "builddeps"
  }
}
```

- Layers are the pin's `Blobs` list verbatim (index blob + `.deb`s for a
  build-deps pin; orig tarballs for a source pin), each a bare digest+size
  descriptor. **No per-layer title annotation** — unlike outputs, a pin has no
  name→blob map to preserve; the git repo's `sources.yml` / `*-deps.yml` hashes
  remain the authority for what references what (§7b). The manifest exists only
  so the tag roots the blobs against registry GC.
- The pin's human `name` (which includes the `InRelease` timestamp, §6) rides in
  `vnd.gl-ng.pin.name`, so a remote observer can reconstruct the local pin file
  faithfully if ever needed — though the local side deliberately does **not**
  re-materialize pins from the remote (§7b: pull-through fetches input blobs by
  digest, driven by git-repo hashes, never by pin tag).

### 11.6 Pull-through read path (map miss)

When `Store.Map.Get(identity)` misses locally, the pull-through layer (§12.3):

1. `GET /v2/<name>/manifests/build-artifact-<identity>` with
   `Accept: application/vnd.oci.image.manifest.v1+json`. 404 → genuine miss,
   return not-found (unchanged local semantics).
2. For each layer descriptor, `GET /v2/<name>/blobs/sha256:<digest>` and stream
   into `store.Blobs.Store(r)` — which re-hashes on write and so **verifies the
   digest for free** (a corrupt/mismatched blob yields a different hash and the
   manifest reference would dangle; treat a mismatch as a fetch failure).
3. Reconstruct the local manifest: for each layer, emit
   `Output{Name: layer.annotations["org.opencontainers.image.title"], Hash: <digest>}`,
   `SerializeManifest(outputs)` (`manifest.go:13`), `store.Blobs.Store` it →
   local manifest hash `m`.
4. `store.Map.Set(identity, m, validate=true)` — now a permanent local hit.

Input blobs (`.deb`, orig, lockfile index) are **not** pulled via pin tags. They
are fetched **by digest** (`GET /v2/<name>/blobs/sha256:<h>`) exactly when some
by-digest consumer (lockfile install, source assembly) asks the blob store for a
hash it lacks — see §12.2 for where that hook lives. Pin tags exist purely to
keep the registry from GC-ing those blobs; the local side never reads them.

### 11.7 What is deliberately *not* on the registry

- The local manifest blob bytes (§11.2) — reconstructed, not transported.
- Pin *files* — the remote has pin *manifests*; the local `pins/<id>.yml` is a
  separate representation that merely agrees on id and name (§7b).
- `map/` entries as such — an output tag *is* the remote map entry; there is no
  separate remote map object.
- Cache-only blobs — `InRelease` cookie cache, `Packages.gz`/`Sources.gz`
  (§8). These are never published and never pulled; a miss re-downloads from the
  Debian mirror as today.

---

## 12. Implementation plan — `gl` binary

Scope: the OCI client, the pull-through hook inside the object store, and a
publish (`gl cache push`) command. Builds strictly on top of the completed
local half (§10). Follow the
[doc-and-tests rule](../guidelines/doc-and-tests-rule.md); each step names its
test obligation. Read before starting: `internal/objstore/{store,blobs,mapstore,pins}.go`,
`internal/artifact/manifest.go`, `cmd/gl/cache.go`, `cmd/gl/util.go`
(`openStore`), and re-verify every signature — they are quoted from the
2026-09-28 tree.

### Step 1 — `internal/ociclient`: a minimal registry client

New package `internal/ociclient` over stdlib `net/http` (no new go.mod dep
beyond what the registry-auth story eventually needs — deferred, §9). It speaks
just the OCI distribution v2 verbs we use:

```go
type Client struct { base string; name string; http *http.Client /* + auth, later */ }

func New(registry, name string) *Client            // registry e.g. "localhost:5000"

func (c *Client) HasBlob(h objstore.Hash) (bool, error)                 // HEAD .../blobs/sha256:<h>
func (c *Client) PushBlob(h objstore.Hash, r io.Reader, size int64) error // POST+PUT monolithic
func (c *Client) PullBlob(h objstore.Hash) (io.ReadCloser, int64, error)  // GET .../blobs/sha256:<h>
func (c *Client) PutManifest(tag string, body []byte, mediaType string) error // PUT .../manifests/<tag>
func (c *Client) GetManifest(tag string) (body []byte, mediaType string, err error) // GET; ok=false on 404
func (c *Client) ListTags() ([]string, error)                            // GET .../tags/list (paginate)
```

- Digest strings are `"sha256:" + h.String()`; parse the reverse with
  `strings.TrimPrefix` + `objstore.NewHash`.
- **Manifest structs**: define Go types for the image manifest, descriptor, and
  the annotation keys (§11.4/§11.5) in a `manifest.go` within this package.
  Marshal deterministically (`encoding/json`; field order is struct order, which
  is fine — registries do not require canonical JSON, only that the pushed bytes
  match the digest *if* you reference the manifest by digest; we reference by
  tag, so byte-canonicality is not load-bearing).
- The empty-config blob (`{}`, `sha256:44136fa3…`) is a package constant; push
  it lazily before the first manifest that needs it.
- **Tests (mandatory, against the local registry from §13):** blob round-trip
  (push→has→pull, digest verified); manifest put→get→parse; `ListTags` prefix
  filtering; 404 on a missing manifest returns not-found, not an error. Gate
  these behind a build tag or `GL_TEST_REGISTRY` env so the default `go test`
  (no registry) stays hermetic — mirror how stub-dependent tests already gate on
  `GL_EXEC_ENV_STUB`.

### Step 2 — pull-through hook in the object store

The read-miss fallthrough must live where every lookup already funnels, without
the artifact layer knowing about OCI. Two miss points (§11.6):

- **Map miss** (`MapStore.Get` returns not-found): reconstruct-from-tag path.
- **Blob miss** (`Blobs.Open` on an absent hash): by-digest pull.

Design decision — **wrap, don't thread a client through every call site.** Add
an optional remote to the `Store`:

```go
type Store struct { root string; Blobs *Blobs; Map *MapStore; Pins *Pins; remote Remote /* nil = pure local */ }
type Remote interface {
    PullBlobByDigest(h Hash) (io.ReadCloser, int64, error)      // 404 → ErrNotFound
    PullOutputManifest(identity Hash) (leaves []Output, ok bool, err error)
}
```

- `Blobs.Open(h)`: on local ENOENT, if the owning `Store` has a `remote`, pull
  by digest, `Store` it (re-hash verifies), then re-open. Requires `Blobs` to
  reach its `Store`'s remote — give `Blobs`/`MapStore` a back-pointer set in
  `Open()`, or (cleaner) move the fallthrough into thin `Store.OpenBlob` /
  `Store.MapGet` wrappers and have callers use those. **Pick the wrapper
  approach**: it keeps `Blobs`/`MapStore` policy-free (consistent with
  `Sweep` being dumb, §5) and confines OCI knowledge to `Store` + `ociclient`.
- `Store.MapGet(identity)`: local `Map.Get`; on miss and `remote != nil`, call
  `PullOutputManifest`, and on `ok` do the reconstruct-and-`Map.Set` of §11.6,
  then return the fresh manifest hash.
- The remote is constructed from config/env (§13 uses `GL_REGISTRY`); a nil
  remote is the current pure-local behavior, so existing tests are unaffected.
- **Audit call sites**: replace direct `store.Blobs.Open` / `store.Map.Get` at
  the read paths that should fall through (artifact build input resolution,
  manifest parsing) with the `Store` wrappers `OpenBlob` / `MapGet`. Leave
  `gl cache blobs get` / `map get` on the *direct* local API — cache-admin
  commands must show local truth, not silently pull.
- **Not every fall-through read goes through `Open`/`Get`.** Rootfs assembly and
  the lockfile `.deb` install paths do **not** read blobs via `Blobs.Open`: they
  need a *filesystem path* to bind-mount or hand to `dpkg-deb`, so they call
  `Blobs.Path(h)` after gating on `Blobs.Has(h)`
  (`internal/build/rootfs.go`, `internal/build/mount_helpers.go`,
  `internal/build/build_phases.go`, `internal/install/{bootstrap,install}.go`).
  Hooking only `OpenBlob`/`MapGet` would silently miss these. A third wrapper
  covers them: `Store.EnsureBlob(h) error` — a no-op if the blob is already
  local or no remote is set, otherwise a pull-by-digest that materializes it
  into the local store (re-hash verifies). Each such site calls `EnsureBlob(h)`
  immediately before its `Blobs.Has` guard / `Blobs.Path` read. The three §11.6
  fall-through paths (map miss, blob-open miss, blob-path miss) thus map to the
  three wrappers `MapGet`, `OpenBlob`, `EnsureBlob`. Importer / apt-repo /
  resolve reads stay on the direct local API — those are re-downloadable caches,
  not published pull-through inputs (§11.7).
- **Tests (mandatory):** with a seeded local registry and an empty local store,
  a `MapGet` for a published identity reconstructs the manifest + pulls all
  leaves + sets the map, and the reconstructed local manifest is byte-identical
  to the original `SerializeManifest` output; a blob `Open` miss pulls by digest
  and verifies; a genuinely-absent identity/blob still reports not-found; a
  nil-remote store behaves exactly as today (regression guard).

### Step 3 — `gl cache push` (publish)

New subcommand under the existing hand-rolled `cmdCache` switch
(`cmd/gl/cache.go:23`; no cobra). It publishes **both** classes (§7):

```text
gl cache push [--registry <r>] [--conf-dir <d>] [--arch <a>] [--stub <p>]
              [--outputs] [--pins] [--dry-run]
```

- Default (`--outputs --pins` both implied when neither given): push everything
  the local store can.
- **Outputs**: build the graph (`build.BuildGraph` → `Discover`, same as
  `cacheGC`), for each node `ResolveOutputRefs`; on error skip (unbuilt). For
  each built node: `HasBlob` each leaf → `PushBlob` the misses → assemble the
  §11.4 manifest → `PutManifest("build-artifact-"+identity, …)`. Skip if the
  tag already exists and `--force` is not set (cheap idempotency: `GetManifest`
  → present → skip).
- **Pins**: `store.Pins.List()`; for each, `PushBlob` missing blobs → §11.5
  manifest → `PutManifest("import-"|"builddeps-"+id, …)`. The kind prefix comes
  from the pin's `Kind` field (`objstore.PinKindImport` / `PinKindBuildDeps`),
  recorded at pin-creation time: `gl import` creates `import` pins, both
  `gl lockfile` / `gl lockfile-rootfs` build-deps sites create `builddeps` pins.
  A pin file written before `Kind` existed loads as `import` for back-compat.
- `--dry-run`: list what would be pushed (tags + blob counts), push nothing.
- Store root from `GL_CACHE` via `openStore("")` (unchanged); registry from
  `--registry` or `GL_REGISTRY`.
- **Tests (mandatory):** against the §13 registry — push a built graph, then
  assert every expected `build-artifact-<id>` tag exists and each manifest's
  layers match `ResolveOutputRefs`; push pins and assert `import-`/`builddeps-`
  tags with the right layer sets; re-push is idempotent (no duplicate blobs,
  tags unchanged); `--dry-run` pushes nothing. Round-trip: push from store A,
  point an empty store B's remote at the registry, `MapGet` an identity, assert
  a full local hit — this is the end-to-end pull-through proof and the highest-
  value test.

### Step 4 — wiring & config surface

- `GL_REGISTRY` env (and `--registry` flag on push) select the registry+repo,
  e.g. `localhost:5000/gl-ng`. Absent → no remote (pure local, pull-through
  disabled). Document precedence like `GL_CACHE`.
- `gl cache status` gains a line: configured registry (or "none"), and — cheap —
  whether it is reachable (`GET /v2/`). Optional; keep it non-fatal.
- **Docs to update with this work**: `doc/src/guide/cache.md` (`push`, the
  `GL_REGISTRY` env, pull-through in the `gc`/miss narrative),
  `doc/src/concepts/object-store.md` ("Mountable as a remote" bullet is now
  real), this page's status note, and a new
  `doc/src/internals/foundations/ociclient.md` for the client package.

### Ordering

Step 1 is standalone (needs only §13's registry to test). Step 2 depends on 1.
Step 3 depends on 1 (and the recommended pin-`Kind` prerequisite). Step 4 is
polish on 2+3. The Step-3 round-trip test is the acceptance gate for the whole
remote half.

---

## 13. Testing locally without GHCR — a real registry in this container

The plan must be provable on the dev box, which is **inside a `podman`
container** (`/run/systemd/container` = `podman`). Running the registry as a
*nested container* is therefore out. Two facts make a clean local test possible:

1. **systemd user services work here** — the session is lingering
   (`loginctl show-user` → `Linger=yes`, `State=lingering`,
   `XDG_RUNTIME_DIR=/run/user/1000`), so `systemctl --user` can run a long-lived
   service with no root and no container.
2. **Debian stable ships the CNCF registry as a plain binary** —
   `apt-get install docker-registry` (2.8.3, ~4.7 MB, zero extra deps) installs
   `/usr/bin/docker-registry` (the `github.com/distribution/distribution`
   `registry serve` binary), a default `/etc/docker/registry/config.yml`, and a
   *system* unit we ignore. It is a single static Go binary — exactly the
   upstream OCI reference registry, so ORAS/OCI conventions are fully honoured,
   including `storage.delete.enabled: true` for exercising registry-side GC.

### 13.1 One-time setup

```bash
sudo apt-get update && sudo apt-get install -y docker-registry
```

Write a **user** config that removes the default htpasswd auth (local test
only), binds a high port, and stores under a writable dir — do **not** reuse the
packaged `/etc/docker/registry/config.yml` (it enables `auth.htpasswd` and roots
storage at `/var/lib/docker-registry`, which a non-root user cannot write):

```yaml
# ~/.config/gl-ng-registry/config.yml
version: 0.1
log: { level: warn }
storage:
  filesystem: { rootdirectory: /home/dev/.local/share/gl-ng-registry }
  delete: { enabled: true }        # needed to test registry-side deletes/GC
http:
  addr: 127.0.0.1:5000
health:
  storagedriver: { enabled: false }
```

(No `auth:` block → anonymous push/pull, correct for a loopback test registry.)

### 13.2 systemd user unit

```ini
# ~/.config/systemd/user/gl-ng-registry.service
[Unit]
Description=Local OCI registry for gl-ng tests
After=network.target

[Service]
ExecStart=/usr/bin/docker-registry serve %h/.config/gl-ng-registry/config.yml
Restart=on-failure

[Install]
WantedBy=default.target
```

```bash
systemctl --user daemon-reload
systemctl --user enable --now gl-ng-registry.service
curl -fsS http://127.0.0.1:5000/v2/ && echo "  registry up"   # → 200 {}
```

`GL_REGISTRY=localhost:5000/gl-ng` then targets it. `systemctl --user status`
/ `journalctl --user -u gl-ng-registry` for logs; `systemctl --user restart` to
reset in-memory state (filesystem storage persists across restarts).

### 13.3 Verifying by hand (independent of `gl`)

`skopeo` (also in Debian stable) cross-checks what `gl cache push` produced
without trusting our own client:

```bash
skopeo list-tags --tls-verify=false docker://localhost:5000/gl-ng
skopeo inspect --raw --tls-verify=false \
  docker://localhost:5000/gl-ng:build-artifact-<identity> | jq .
```

Assert the raw manifest matches §11.4 (media types, empty config, one layer per
leaf with the `org.opencontainers.image.title` = output name). A blob spot-check:
`curl -fsS http://127.0.0.1:5000/v2/gl-ng/blobs/sha256:<h> | sha256sum` must
echo `<h>` — the registry stores by the same digest the local store uses.

### 13.4 Cleaning between runs

Wiping registry state is `rm -rf ~/.local/share/gl-ng-registry/*` (or
`systemctl --user stop` first). Registry-side GC (to test that tags root blobs
and untagged blobs collect) is `docker-registry garbage-collect
~/.config/gl-ng-registry/config.yml` — run against the stopped service. For a
fully hermetic Go test, point storage at a `t.TempDir()`-style path via a
per-test config and a throwaway port, or simply gate the registry-dependent
tests on `GL_TEST_REGISTRY` (§12 Step 1) and run them against this one shared
service in CI/dev, keeping `go test` with no env hermetic and offline.

### 13.5 Why not the alternatives

- **Nested container registry** (`podman run registry`): blocked — we are
  already in a `podman` container and cannot assume nested containers work; the
  user ruled this out explicitly.
- **A pure-Go in-process registry in the test binary** (e.g.
  `go-containerregistry`'s `registry` package): attractive for hermeticity and a
  reasonable *future* addition, but pulls a new go.mod dependency into a module
  that currently has almost none, and tests our client against a *different*
  implementation than production (GHCR). The `docker-registry` binary is the
  same reference implementation the ecosystem targets, costs one `apt-get`, and
  needs no dependency change — preferred for now. Revisit an in-process registry
  only if the systemd-user approach proves flaky in CI.
