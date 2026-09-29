# `dirhash` — Deterministic Directory Hashing

`dirhash` reduces a directory tree to a single SHA-256 hex string. The hash changes if and only if the tree's content, names, types, or executable bits change. It's used to derive artifact identities from package source directories.

Single file: 189 lines (`hash.go`).

## Public surface

```go
func HashDirectory(path string) (string, error)
```

`HashDirectory` returns a 64-char lowercase hex string. There used to be a sibling `dirhash.ConcatHash` here for callers that work in hex form, but the duplicate has been removed — `objstore.ConcatHash` is the single composition primitive in the codebase. Callers that want a hex string convert from `objstore.Hash` via `String()`.

## Type bytes

Each entry's hash starts with a one-byte type tag:

```go
const (
    TypeRegular    byte = 0
    TypeExecutable byte = 1
    TypeDirectory  byte = 2
    TypeSymlink    byte = 3
    TypeSpecial    byte = 4
)
```

These are deliberately stable — types 0–3 were chosen to match a Python reference implementation, so a hash computed in Python over the same tree must equal the Go result for those kinds. `TypeSpecial` was added later for FIFOs / sockets / device nodes; trees that contain none of those still hash identically to the Python reference.

## Algorithm

```mermaid
flowchart TD
    A[HashDirectory path] --> B[os.OpenRoot path]
    B --> C[hashDir at .]
    C --> D[ReadDir]
    D --> E[Sort lexicographically]
    E --> F{For each entry}
    F --> G[Lstat]
    G --> H{Type?}
    H -->|file| I[hashFile = SHA-256 of bytes]
    H -->|dir| J[hashDir recursively]
    H -->|symlink| K[Readlink, SHA-256 of target string]
    H -->|other| L[SHA-256 of mode.Type string]
    I --> M[Feed h: typeByte, SHA-256 name, content hash]
    J --> M
    K --> M
    L --> M
    M --> F
    F --> N[Return hex h.Sum]
```

For each directory entry, the running hash absorbs three things, in order:

1. The 1-byte type tag.
2. `SHA-256(entry-name)` — 32 bytes. Hashing the name (instead of writing it raw) ensures that no length-prefix or separator is needed.
3. The 32-byte content hash:
   - **Regular**: `SHA-256(file bytes)`. Executable iff `mode & 0100 != 0` — the *user* execute bit, not group/other.
   - **Directory**: recursive `hashDir` result, hex-decoded back to 32 bytes.
   - **Symlink**: `SHA-256(target string)`. The link is *not* followed.
   - **Special** (FIFO, socket, device): `SHA-256(mode.Type().String())`. The entry is not opened — opening would block (FIFO) or fail. The directory hash still changes if the entry is added, removed, renamed, or its kind changes (e.g. FIFO → socket), because the type byte and name digest both feed in.

The final directory hash is `hex.EncodeToString(h.Sum(nil))`.

### Symbolic links

Lstat detects symlinks. The target string is read with `os.Readlink` on the full filesystem path (because `os.Root` does not expose `Readlink`). This is safe — we already confirmed the entry is a symlink, and we never traverse it.

A symlink with an absolute target hashes to the same value regardless of where the tree is located. This is intentional — moving the tree shouldn't change its hash.

### Special file types

Device nodes, sockets, and FIFOs are recorded by name and a hash of `mode.Type().String()`. Their contents are *not* read — opening a FIFO would block, opening a socket or device node either fails or has side effects. Source trees rarely contain these, but a few upstream Debian packages ship FIFOs as test fixtures (e.g., `dpkg`'s `tests/t-unpack-fifo/`), so refusing them outright would block from-source builds of those packages.

## Why `os.OpenRoot`

Plain `os.Open` followed by `filepath.Walk` is vulnerable to symlink races: a malicious symlink within the tree can cause the walk to follow it out of the tree. `os.OpenRoot` (Go 1.24+) returns an `*os.Root` whose subsequent `Open`/`Lstat` calls are confined to the opened directory — symlinks pointing outside the root error out at open time.

This matters because:
- The `internal/importer` package extracts source tarballs that come from external mirrors. A hostile orig.tar can contain `../../etc/shadow` symlinks.
- If we hashed across that boundary, we'd silently mix host filesystem state into artifact identity, breaking caching and reproducibility.

The flip side: `os.Root` has a smaller API than `os.File`, so `Readlink` is done via the host path. That is safe because we read the target string only — we don't *follow* it.

## Tests

Sixteen tests in `hash_test.go`:

- Empty directory: hashes consistently.
- Single file: deterministic, content-sensitive.
- Determinism: same tree → same hash, across multiple calls and calling orders.
- Executable bit: changes hash (different type byte).
- Symlink: present and target both affect hash.
- Nested / deeply nested: recursion works at depth.
- Sort order independence: hash is stable regardless of OS-dependent readdir order.
- Special file (FIFO): hashed deterministically by name + mode-type marker; removing or renaming the FIFO changes the hash.
- Nonexistent path: error from `os.OpenRoot`.
- Empty file vs empty subdir: distinct hashes (different type byte).
- Mixed content: sanity check.

## Gotchas

- **Don't depend on the *exact* hash value across stdlib changes.** `os.OpenRoot`'s underlying behavior is stable, but if a future Go release changes file iteration semantics, the hash may shift. Currently it doesn't.
- **The executable bit detection is `mode & 0100`, not `mode & 0111`.** Group- or other-execute don't change the hash. This matches the Python reference.
- **Hashing a tree that contains itself (cyclic symlink) does *not* loop**, because symlinks are not followed. The cycle becomes an entry with a target string content hash.
