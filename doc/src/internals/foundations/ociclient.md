# `ociclient` — OCI Registry Client & Pull-Through Backend

`internal/ociclient` is the **remote half** of the object store's pull-through
cache ([design](../../concepts/oci-cache-design.md) §11–§13). It is a minimal
OCI distribution v2 client over stdlib `net/http` — no third-party
dependency — plus the ORAS-style manifest layout gl-ng publishes, and the
`Remote` adapter that plugs into `objstore.Store`.

## Files

```
internal/ociclient/
├── client.go     # Client: blob head/push/pull, manifest put/get, tag list
├── manifest.go   # Media types, tag prefixes, manifest structs + builders
└── remote.go     # Remote: adapts Client to objstore.Remote (the store hook)
```

## The wire model

Blobs are content-addressed: an `objstore.Hash` maps to the OCI digest
`"sha256:" + h.String()`, and `Blobs.Store` re-hashes on write, so a pulled blob
verifies its own digest for free. Each published artifact is an ORAS-style OCI
image manifest — an empty config (`{}`, `sha256:44136fa3…`, 2 bytes) plus one
layer per blob — referenced **by tag** (so canonical-JSON byte-exactness is not
load-bearing). A slash is illegal in a tag, so the namespace is a tag prefix:

| Prefix | Tag | Manifest |
|---|---|---|
| `build-artifact-` | `build-artifact-<identity>` | output: one titled layer per leaf, identity in an annotation (§11.4) |
| `import-` | `import-<pin-id>` | pin: bare digest+size layers, pin id/name/kind in annotations (§11.5) |
| `builddeps-` | `builddeps-<pin-id>` | pin, as above; kind = `builddeps` |

## `Client`

```go
func New(registry, name string) *Client   // e.g. New("localhost:5000", "gl-ng")
func SplitRef(ref string) (registry, name string) // "localhost:5000/gl-ng" → parts

func (c *Client) Reachable() bool                                        // GET /v2/
func (c *Client) HasBlob(h objstore.Hash) (bool, error)                  // HEAD blob
func (c *Client) PushBlob(h objstore.Hash, r io.Reader, size int64) error // POST session → PUT ?digest=; skips if present
func (c *Client) PullBlob(h objstore.Hash) (io.ReadCloser, int64, error)  // GET; ErrNotFound on 404
func (c *Client) PutManifest(tag string, body []byte, mediaType string) error
func (c *Client) GetManifest(ref string) (body []byte, mediaType string, ok bool, err error) // ok=false on 404
func (c *Client) ListTags() ([]string, error)                            // follows Link pagination
```

`PushBlob` uploads the shared empty-config blob lazily before the first manifest
that needs it. A 404 is a not-found (`ErrNotFound` / `ok=false`), never an error.

## Manifests

`manifest.go` defines the media-type / artifact-type / annotation-key / tag
constants, the `Descriptor` and `Manifest` structs, and three helpers:

```go
func BuildOutputManifest(identity objstore.Hash, leaves []LeafRef) ([]byte, error)
func BuildPinManifest(kind, id, name string, blobs []objstore.Hash, sizes map[objstore.Hash]int64) ([]byte, error)
func ParseManifest(body []byte) (Manifest, error)
```

An output layer carries its leaf name in the standard
`org.opencontainers.image.title` annotation; that title is what the pull path
turns back into an `objstore.Output.Name`.

## `Remote` — the store hook

```go
func NewRemote(c *Client) *Remote          // var _ objstore.Remote = (*Remote)(nil)
```

`Remote` implements the two-method `objstore.Remote` interface the store falls
through to on a local miss. `cmd/gl` builds it from `GL_REGISTRY` (or
`--registry`) and attaches it with `Store.SetRemote`; the store's `OpenBlob`,
`MapGet`, and `EnsureBlob` wrappers do the rest:

- `PullBlobByDigest(h)` → `Client.PullBlob` — a blob-by-digest pull.
- `PullOutputManifest(identity)` → fetches `build-artifact-<identity>`, parses
  it, and converts each titled layer back into an `objstore.Output`. The store
  then reconstructs the local manifest blob byte-identically (via
  `objstore.SerializeManifest`) and sets the map entry.

`objstore` never imports `ociclient` — the dependency points one way, through
the interface, keeping OCI knowledge out of the store. Publishing (the reverse
direction) lives in `gl cache push` (`cmd/gl/cache.go`), which uses the `Client`
directly.

## Testing

Registry-dependent tests gate on the `GL_TEST_REGISTRY` environment variable
(e.g. `localhost:5000/gl-ng`) and `t.Skip` when it is unset, mirroring how
stub-dependent tests gate on `GL_EXEC_ENV_STUB` — so the default `go test` stays
hermetic and offline. See §13 of the design doc for the local registry setup
(`docker-registry` + `skopeo` under a systemd `--user` unit). The round-trip
test (`roundtrip_test.go`) is the acceptance gate: push an output, point a fresh
empty store's remote at the registry, and confirm `MapGet` reconstructs the
manifest and pulls every leaf. The store's own pull-through unit tests use an
in-memory fake `Remote` and need no registry.
