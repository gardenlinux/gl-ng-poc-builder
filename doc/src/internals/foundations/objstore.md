# `objstore` — Content-Addressed Store

The object store is gl-ng's persistence layer. Three subsystems: blobs (content-addressed file storage), map (identity → blob hash), and pins (GC roots for local-only input blobs).

## Files

```
internal/objstore/
├── hash.go             # Hash type
├── blobs.go            # Blob storage
├── mapstore.go         # Identity map
├── pins.go             # Pins: GC roots for local-only blobs
├── store.go            # Top-level Store, DefaultRoot
└── concat_hash.go      # ConcatHash + CookieKey
```

## The Hash type

```go
type Hash [32]byte

func NewHash(s string) (Hash, error)    // Parse 64-char hex
func MustHash(s string) Hash            // Panic on invalid (tests only)
func (h Hash) String() string           // Full 64-char hex
func (h Hash) Short() string            // First 12 chars (display)
func (h Hash) Prefix() string           // First 2 chars (shard dir)
func (h Hash) Suffix() string           // Chars 3-64 (filename in shard)
func (h Hash) Equal(other Hash) bool    // Constant-time
func (h Hash) IsZero() bool
```

Validates length (64 hex chars) and alphabet (`[0-9a-f]`) at construction. Lowercase only — uppercase hex is rejected.

## Storage layout

```
<root>/
├── blobs/
│   ├── ab/                      # First 2 hex chars
│   │   └── cd1234...            # Remaining 62 chars
│   └── ...
├── map/
│   ├── 12/                      # Identity prefix
│   │   └── 34abcd...            # value: "<value-hash>\n"
│   └── ...
└── pins/
    └── 01166ad6b9eb050e.yml     # one YAML file per pin (16-hex-char id)
```

Two-character sharding caps any directory at 256 buckets — 256×256 = 65,536 max, comfortably below filesystem inode limits.

## Blobs

```go
type Blobs struct { ... }

func (b *Blobs) Has(h Hash) bool
func (b *Blobs) Path(h Hash) string                    // Doesn't verify existence
func (b *Blobs) Open(h Hash) (io.ReadCloser, error)    // Retries chmod 0644 on EACCES
func (b *Blobs) Store(r io.Reader) (Hash, error)
func (b *Blobs) Delete(h Hash) error                   // Best-effort shard cleanup
func (b *Blobs) Iterate(fn func(Hash) error) error
```

### `Store(r)` algorithm

1. `io.TeeReader(r, sha256)` — write to temp file in `blobs/`, hash on the side.
2. Compute final hash from the digest.
3. `os.MkdirAll(blobs/<2>, 0755)` — make shard dir if needed.
4. `os.Rename(tmp, final)` — atomic rename.
5. `os.Chmod(final, 0644)` — normalize permissions.

If the final path already exists (because a concurrent writer beat you to it), the rename overwrites — correct, because it's content-addressed.

### `Open(h)` and the chmod retry

```go
f, err := os.Open(path)
if err != nil && os.IsPermission(err) {
    os.Chmod(path, 0o644)
    f, err = os.Open(path)
}
```

This handles the multi-user case where a previous run wrote a blob with restrictive permissions. Idempotent — if the chmod fails, the second `Open` just returns the same error.

## Map

```go
type MapStore struct { ... }

func (m *MapStore) Has(key Hash) bool
func (m *MapStore) Get(key Hash) (Hash, error)
func (m *MapStore) Set(key, value Hash, validate bool) error
func (m *MapStore) Delete(key Hash) error
func (m *MapStore) Iterate(fn func(key Hash) error) error
```

`Set(key, value, validate=true)` calls `Blobs.Has(value)` first; refuses if the blob is missing. Tests use `validate=false` for speed.

The on-disk format of one map entry is just the value hash followed by a newline. Storage is one file per identity, sharded the same way as blobs.

## Pins

```go
type Pin struct {
    ID    string  // 16-hex-char id (also the pins/<id>.yml basename); yaml:"-"
    Name  string  // free-form origin description; yaml:"name"
    Blobs []Hash  // blobs this pin protects; yaml:"blobs"
}

func (p *Pins) Create(name string, blobs []Hash) (string, error) // returns new id
func (p *Pins) Get(id string) (Pin, error)
func (p *Pins) List() []Pin                          // skips malformed files
func (p *Pins) Drop(id string) error
func (p *Pins) ReachableBlobs() map[Hash]struct{}    // union across all pins
```

Each pin is a separate YAML file at `<root>/pins/<id>.yml`. `Create` generates
a fresh 16-hex-char id from `crypto/rand`, then writes via temp-file +
atomic-rename — there is no shared index file, so concurrent creates do not
contend. `id` must match `^[0-9a-f]{16}$`; `Get`/`Drop` reject anything else.
`List` and `ReachableBlobs` tolerate a malformed or unparseable pin file by
skipping it rather than failing.

Pins are the GC roots for **local-only** blobs — imported orig tarballs
(`gl import` source pins) and lockfile index blobs plus resolved `.deb`s
(`gl lockfile` / `gl lockfile-rootfs` build-deps pins). Protection is
many-to-many: a blob named by two pins survives until both are dropped. GC keeps
a blob iff it is graph-reachable **or** in `ReachableBlobs()`. See
[the object-store concept page](../../concepts/object-store.md) and
[oci-cache-design.md](../../concepts/oci-cache-design.md).

## Top-level Store

```go
type Store struct {
    Blobs *Blobs
    Map   *MapStore
    Pins  *Pins
}

func Open(root string) (*Store, error)              // "" → DefaultRoot()
func DefaultRoot() string                           // GL_CACHE env, else ~/.cache/gl-ng
func (s *Store) Root() string
```

`Open` makes the root path absolute (critical because the store is consulted from inside MountNS where CWD is `/`).

## ConcatHash

```go
func ConcatHash(parts ...string) Hash
```

Composes artifact identities from multiple sub-hashes. Algorithm:

1. For each input string `parts[i]`, compute `digest_i := SHA-256(parts[i])` — 32 bytes.
2. Concatenate `digest_0 || digest_1 || ... || digest_n`.
3. Return `SHA-256` of the concatenation.

Used everywhere identity is composed: source-build identity from dir-hash + arch + dep identities; rootfs identity from name + arch + lockfile-hash + dep identities; etc.

## CookieKey

```go
func CookieKey(cookie, repoURL, dist string) Hash
```

Specialised `ConcatHash` for HTTP-fetch caching. Used by importer and lockfile generator: the same `(cookie, repoURL, dist)` tuple always produces the same key, so any cached `InRelease` content keyed by that hash is reusable across calls within a session.

## Notable design choices

- **No size limits.** Blobs can be hundreds of MB (`.deb` files for gcc-16 are 200+ MB) and rootfs tarballs can be GB-scale. The store is just a filesystem — bounded by disk.
- **No locking between processes.** Writes are atomic via `rename`; concurrent writers to the same hash produce identical content, so the last one to rename wins harmlessly.
- **Pins are one file per pin, not a structured index.** Map is likewise one-file-per-key. There is no SQLite, no LMDB.
- **Permission recovery in `Open`.** Multi-user setups (CI shared with developer) sometimes leave blobs with restrictive permissions; the retry handles this without forcing each caller to chmod.

## Tests

- Hash: parse, validate, reject non-hex, reject wrong length, prefix/suffix, stringer.
- Blobs: store/retrieve, store empty, store large, store duplicate idempotency, delete, iterate, atomicity (parallel store of same content).
- Map: set/get, validate-on-set, overwrite, delete, iterate.
- Pins: create/list/get round-trip, unique ids, `ReachableBlobs` dedup across pins, drop semantics (shared blobs survive), malformed file skipped, invalid-id rejection, build-deps auto-pin shape.
- ConcatHash: order matters, deterministic, different inputs → different outputs.
- Store: open creates dirs, env override, end-to-end with all three subsystems.

## Gotchas

- **Don't construct `Hash` literals in code** — use `NewHash` or `MustHash`. Direct `Hash{...}` literals can be malformed.
- **`Blobs.Path(h)` returns a path even if the blob doesn't exist.** Always pair with `Has(h)` if existence matters.
- **Map values are blob hashes, not direct content.** The pattern is `value := Map.Get(id); blob := Blobs.Open(value)`.
