# Importing Sources

`gl import` fetches a Debian source package and writes it into a conf-dir.

## Usage

```text
gl import [flags] <package-name>

Flags:
  --repo string       APT repository URL (default "https://deb.debian.org/debian")
  --dist string       Distribution (default "testing")
  --keyring string    GPG keyring path (default /usr/share/keyrings/debian-archive-keyring.gpg)
  --cache string      Object store directory (default ~/.cache/gl-ng)
  --output string     Conf-dir to write into (default ".")
  --no-verify         Skip GPG signature verification
  --cookie string     InRelease cache cookie (reuse cached InRelease across invocations)
```

## Example

```bash
gl import --output ./staging --cookie "$(uuidgen)" bash
```

Result on disk:

```
./staging/pkgs/bash/
├── sources.yml
└── src/
    └── debian/
        ├── changelog
        ├── control
        ├── rules
        ├── patches/
        └── ...
```

Plus, the orig tarball stored as a blob in `~/.cache/gl-ng/blobs/`.

## What `gl import` does

1. Fetch and (optionally) GPG-verify `InRelease` for the configured repo+dist.
2. Fetch `Sources.gz`, parse it, and pick the **latest** version of the requested package. (Debian testing is rolling — you may import a different version on different days.)
3. Read the `Files:` field for the chosen source stanza to find the `.dsc`, `orig.tar.*`, and (for 3.0 quilt or 1.0) the patch tarball.
4. Download all referenced files; verify their hashes against the `Sources` index.
5. Detect format (`3.0 (quilt)`, `3.0 (native)`, or `1.0`) and unpack:
   - **3.0 (quilt)**: extract the `.debian.tar.*` into `pkgs/<name>/src/debian/`. Store the orig tarball as a blob.
   - **3.0 (native)**: extract the whole tarball into `pkgs/<name>/src/`. No orig tarball blob.
   - **1.0**: extract the orig tarball, apply the `.diff.gz` via `patch(1)`, take the resulting `debian/` tree. Store the orig as a blob.
6. Write `pkgs/<name>/sources.yml` referencing each orig tarball blob by hash.

## The `--cookie` flag

`InRelease`, `Sources.gz`, and `Packages.gz` are large and don't change within a session. The cookie key is folded into a cache identity so that a second `gl import` (or `gl lockfile`) with the *same* `--cookie` reuses the parsed/decompressed content without re-fetching. Without `--cookie`, every fetch is fresh.

The convention in `prepare_staging.sh`:

```bash
COOKIE="$(uuidgen)"
gl import --cookie "$COOKIE" pkg1
gl import --cookie "$COOKIE" pkg2
gl lockfile --cookie "$COOKIE" pkg1
gl lockfile --cookie "$COOKIE" pkg2
```

…issues one network round-trip for `InRelease`+`Sources`+`Packages`, even across many subcommands.

## Re-importing

Re-running `gl import` for an already-imported package overwrites the package directory and re-stores any orig tarball blobs. This is safe — the orig tarball is content-addressed, so re-storing produces the same blob hash. (Re-import is most useful when an upstream version has changed and you want to refresh the source tree.)

## Importing without GPG verification

`--no-verify` skips signature verification. Use this in CI environments that don't have the Debian keyring installed, or for offline development. Phase 1 also has a path that strips the cleartext signature wrapper from `InRelease` when verification is skipped, so the parser doesn't choke on the surrounding PGP markup.

## What `gl import` does *not* do

- **No source patches are applied.** What you get is the unmodified `debian/` directory (and full source tree for native packages).
- **No build is triggered.** Importing only stages the source.
- **No lockfile is generated.** Run `gl lockfile` separately.
- **No git operations.** Phase 1's conf-dir is just a directory; the import doesn't commit, branch, or tag. (Phase 2 will replace this with a Git-based import.)

## Errors and what to do about them

- *"package not found in Sources"* — the spelling is wrong, or you're pointing at a distribution that doesn't carry it. Try `--dist unstable` or check the package exists with `apt-cache showsrc <name>`.
- *"GPG verification failed"* — your keyring is stale. Either update `debian-archive-keyring`, point `--keyring` at a current keyring, or pass `--no-verify` if you accept the risk.
- *"hash mismatch"* — the mirror served corrupt data. Retry; if it persists, switch mirrors with `--repo`.

## See also

- [Concept: Source Management](../concepts/sources.md) — why imports come from the APT archive, not Salsa.
- [Generating Lockfiles](./lockfile.md) — what to run after import.
- [Importer internals](../internals/build/importer.md) — the implementation.
