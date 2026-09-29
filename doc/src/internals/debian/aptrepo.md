# `debian/aptrepo` — APT Repository Metadata Fetch

`internal/debian/aptrepo` owns the small slice of APT-repository handling that is shared between two otherwise-unrelated callers:

- `internal/importer`, which walks a `Sources` index to find a source package, and
- `internal/lockfile`, which walks a `Packages` index to resolve build-deps.

Both need to do the same three things up front: fetch the `InRelease` file, optionally verify its GPG signature, and read the `SHA256:` table out of the resulting Release stanza. The package factors that prelude out so neither caller has to re-derive it.

## Surface

```go
type FetchConfig struct {
    Store      *objstore.Store
    RepoURL    string
    Dist       string
    Cookie     string
    Keyring    string
    NoVerify   bool
    HTTPClient *http.Client
    Component  log.Component
}

func FetchInRelease(ctx context.Context, cfg FetchConfig) ([]byte, error)
func ParseReleaseHashes(payload []byte) (map[string]string, error)
```

`FetchInRelease` returns the *Release payload* — the body of the cleartext-signed envelope, suitable for passing straight into `ParseReleaseHashes` (or any deb822 reader). `ParseReleaseHashes` returns `path → hex SHA-256` for every entry in the `SHA256:` field.

## Caching: the Cookie

`FetchConfig.Cookie`, when non-empty, keys an `objstore.Map` entry that caches the downloaded `InRelease` bytes across invocations. Callers compose cookies from whatever trust context they care about — typically a release date string. On a hit, `FetchInRelease` reads the bytes from the blob store and skips the HTTP round trip; on a miss, it downloads, stores the blob, and writes the map entry.

When `Cookie` is empty the file is always re-downloaded.

## Trust

If `NoVerify` is false and `Keyring` is set, `FetchInRelease` runs the bytes through `stream.GPGVerifyClearSigned` and returns the verified payload. Otherwise it strips the cleartext envelope via `stream.ExtractClearSignedPayload` without verifying — used only when callers have an out-of-band trust path.

## Logging

`Component` selects which `log.Component` tag the package uses for its own log lines. Importer passes `log.Importer`; lockfile passes `log.Lockfile`. When zero, defaults to `log.Fetch`.

## Code references

- `internal/debian/aptrepo/aptrepo.go:64` — `FetchInRelease`
- `internal/debian/aptrepo/aptrepo.go:103` — `loadOrDownloadInRelease` (cookie-cache lookup + download path)
- `internal/debian/aptrepo/aptrepo.go:154` — `ParseReleaseHashes`
- `internal/importer/import.go` — calls `FetchInRelease` + `ParseReleaseHashes` to find Sources.gz
- `internal/lockfile/generate.go` — calls the same pair to find Packages.gz
