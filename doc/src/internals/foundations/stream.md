# `stream` — Subprocess Pipes for Decompress, Tar, GPG, Hash

`stream` is the I/O glue between gl-ng and the host's compression / archive / signing tools. Rather than carry compression in-process, it spawns `xz`, `gzip`, `bzip2`, `zstd`, `tar`, and `gpgv` as child processes and pipes data through them. Hashing is in-process (`crypto/sha256`).

```
internal/stream/
├── process.go           # procReadCloser (pipe helper)
├── decompress.go        # XZ/Gzip/Bzip2/Zstd + AutoDecompress
├── tar.go               # TarExtract, TarCreate, TarList, TarExtractFile
├── gpg.go               # GPGVerify (detached) + GPGVerifyClearSigned
├── hash.go              # HashReader, HashWriter, SHA256Reader, SHA256Bytes
└── deb.go               # DebExtractData (ar parser)
```

## Phase 1 caveat

This package uses `os/exec` directly. Every other build-time subprocess in the system goes through `ExecEnv`. Stream is the documented exception — see CLAUDE.md, decision log under "ExecEnv constraint". When the system grows a remote/VM execution backend, the stream primitives will need to route through ExecEnv too.

## `procReadCloser` — the helper

Internal type that wraps `exec.Cmd`. It pipes a reader into stdin, exposes stdout as `io.ReadCloser`, captures stderr to a buffer, and on `Close()`:

1. Closes the stdout pipe (signals "done reading" to the child).
2. Calls `cmd.Wait()`.
3. If the child exited non-zero, returns an error including the stderr content.

`Close` is idempotent — `done` flag prevents a double-Wait.

```go
func newProcReadCloser(inputReader io.Reader, name string, args ...string) (*procReadCloser, error)
```

All the decompressors are 3-line wrappers around this.

## Decompression

```go
func XZDecompress(r io.Reader) (io.ReadCloser, error)     // xz -d -c
func GzipDecompress(r io.Reader) (io.ReadCloser, error)   // gzip -d -c
func Bzip2Decompress(r io.Reader) (io.ReadCloser, error)  // bzip2 -d -c
func ZstdDecompress(r io.Reader) (io.ReadCloser, error)   // zstd -d -c

func AutoDecompress(r io.Reader, filename string) (io.ReadCloser, error)
```

`AutoDecompress` switches on the filename suffix (`.xz`, `.gz`, `.bz2`, `.zst`). Unknown suffix returns an error rather than a passthrough — the caller is expected to know the data is compressed.

## Tar

```go
func TarExtract(r io.Reader, targetDir string, stripComponents int) error
func TarExtractCompressed(r io.Reader, filename, targetDir string, stripComponents int) error
func TarCreate(dir string, w io.Writer) error
func TarList(r io.Reader) ([]string, error)
func TarExtractFile(r io.Reader, path string) ([]byte, error)
```

`TarExtract` is the workhorse — used by the importer to land orig tarballs and by the build chroot to land .deb data.tar payloads.

`TarCreate` is the rootfs tarball builder. It takes determinism seriously:

```go
exec.Command("tar", "--mtime=@0", "--numeric-owner", "--sort=name", "-c", "-C", dir, ".")
```

- `--mtime=@0` — every entry's mtime becomes the epoch.
- `--numeric-owner` — UIDs/GIDs go in as numbers (no user/group name lookup).
- `--sort=name` — entries appear in lexical order.

These three flags together make `TarCreate` byte-for-byte deterministic given a deterministic input tree, which is verified by `tar_determinism_test.go`.

`TarExtractFile` uses `tar -xO <path>` to pipe a single file's content to stdout and returns it as `[]byte`. Used by the importer to read `debian/control` from inside a `.dsc`-referenced tarball without extracting.

## GPG

```go
func GPGVerify(keyringPath, signaturePath, dataPath string) error
func GPGVerifyClearSigned(keyringPath string, signedData io.Reader) ([]byte, error)
func ExtractClearSignedPayload(data []byte) ([]byte, error)
```

`GPGVerify` runs `gpgv --keyring <kr> <sig> <data>`. No frills — exit code is the answer.

`GPGVerifyClearSigned` handles Debian `InRelease` files (cleartext-signed Release stanzas):

1. Read all of `signedData` into memory.
2. Write to a temp file.
3. `gpgv --keyring <kr> <tmp>` — verifies the signature.
4. `extractClearSignedPayload` strips the envelope and returns the bare Release stanza bytes.

`ExtractClearSignedPayload` (exported) does step 4 *without verification*. Used when the caller has explicitly opted out of GPG (e.g., `gl import -no-verify`), but still needs the Release content out of the cleartext envelope.

The envelope-stripping logic:

```
-----BEGIN PGP SIGNED MESSAGE-----
Hash: SHA256
                        ← blank line
<payload>
-----BEGIN PGP SIGNATURE-----
...
-----END PGP SIGNATURE-----
```

Steps: find the `BEGIN PGP SIGNED MESSAGE` marker, skip to the first blank line (handle both `\n\n` and `\r\n\r\n`), capture up to `BEGIN PGP SIGNATURE`, trim trailing newlines, un-dash-escape any line starting with `"- "` (RFC 4880 §7.1).

## Hashing

```go
type HashReader struct { ... }
func NewHashReader(r io.Reader) *HashReader
func (hr *HashReader) Read(p []byte) (int, error)
func (hr *HashReader) Sum() string

type HashWriter struct { ... }
func NewHashWriter(w io.Writer) *HashWriter
func (hw *HashWriter) Write(p []byte) (int, error)
func (hw *HashWriter) Sum() string

func SHA256Reader(r io.Reader) (string, error)
func SHA256Bytes(data []byte) string
```

`HashReader`/`HashWriter` are the "tee into a hash" pattern as types — wrap an underlying reader/writer, accumulate SHA-256 of every byte that passes through. `Sum()` returns the running hex digest at any point and is idempotent.

`SHA256Reader` is "hash this whole stream and discard the data" — used when only the digest matters.

## `DebExtractData`

```go
func DebExtractData(r io.Reader) (io.Reader, string, error)
```

A `.deb` is an `ar` archive with three members: `debian-binary`, `control.tar.*`, `data.tar.*`. This function walks the ar headers (60-byte fixed-format records, magic `\`\n` at offset 58–59) and returns:

1. A reader over the `data.tar.*` member's bytes (loaded fully into memory — typical sizes are megabytes).
2. The member's filename (`data.tar.xz`, `data.tar.zst`, etc.) — needed for `AutoDecompress`.

`control.tar.*` is *not* exposed by this function — the importer doesn't need it because it parses `debian/control` from the source tree, and the rootfs builder doesn't need it because dpkg internals are reconstructed from the binary index.

## Tests

- `decompress_test.go` (10): roundtrips for all four decompressors, AutoDecompress dispatch, invalid data, empty input, large data, path-like filenames.
- `tar_test.go` (7): basic extract, strip components, invalid tar, empty tar, permissions, compressed extract, unsupported extension.
- `tar_determinism_test.go` (3): `TarCreate` is byte-stable across runs, mtimes are epoch, owners are numeric.
- `gpg_test.go` (9): detached + cleartext-signed verify (with real keyrings in `testdata/`), tampered data, dash-escaping, missing markers.
- `hash_test.go` (13): HashReader/Writer correctness vs `SHA256Reader`, idempotent Sum, empty input, partial writes.
- `process_test.go` (7): success path, large data, non-zero exit propagates stderr, invalid command, transform pipe (`tr a b`), double-close.

No deb_test.go — `DebExtractData` is exercised by build/install integration paths.

## Gotchas

- **Decompressors block on Close** until the child exits. If the caller stops reading partway through and forgets to `Close()`, the child stays alive holding stdin open. Always `defer rc.Close()`.
- **`procReadCloser.Close` only returns the *exit* error** — if the underlying reader is malformed mid-stream, you'll see EOF on Read and an error on Close, in that order.
- **`TarCreate` does not compress.** It writes raw tar to the writer. Pipe through `gzip` separately if you want `.tar.gz`.
- **The `tar`/`gpgv`/`xz`/etc. binaries are looked up via `$PATH`.** Build environments missing these tools fail at run time, not at link time.
