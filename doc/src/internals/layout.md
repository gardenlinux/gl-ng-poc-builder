# Repository Layout

A walking tour of `gl-ng/`.

## Top level

```
gl-ng/
├── cmd/                    # Binary entry points
│   ├── gl/                 # The main CLI binary
│   ├── exec_env_stub/      # The namespace stub binary (forked into namespaces)
│   ├── logdemo/            # Standalone demo for the log package
│   └── taskdemo/           # Standalone demo for the taskui package
├── internal/               # All non-public Go packages
│   ├── artifact/           # Graph engine
│   ├── build/              # Concrete artifact types (DebianPkgBuild, BinaryPkg, Rootfs)
│   ├── container/          # ExecEnv layers (BaseExecEnv, UserNS, MountNS, Container)
│   ├── debian/             # Debian format handling
│   │   ├── deb822/
│   │   ├── depends/
│   │   ├── index/
│   │   └── version/
│   ├── dirhash/            # Deterministic directory hashing
│   ├── importer/           # Source import (3.0 quilt, 3.0 native, 1.0)
│   ├── install/            # dpkg install + bootstrap helpers
│   ├── ipc/                # SOCK_SEQPACKET RPC protocol
│   ├── lockfile/           # Lockfile generation
│   ├── log/                # Structured logging
│   ├── objstore/           # Content-addressed blob store + identity map
│   ├── resolver/           # Backtracking dependency resolver
│   ├── stream/             # Subprocess pipe primitives (decompress, tar, gpg)
│   └── taskui/             # Interactive task progress UI
├── tests/                  # Integration test scripts and templates
│   ├── full_build_test.sh
│   ├── prepare_staging.sh
│   └── templates/
├── doc/                    # This documentation (mdbook)
│   ├── book.toml
│   └── src/
├── go.mod
└── go.sum
```

## Module path

The Go module is `gl-ng` (no domain prefix). All internal imports use `gl-ng/internal/<pkg>`. Phase 1 is intentionally not published as a library — every consumer is in this same repo.

## Where things execute

`gl` (the binary in `cmd/gl/`) is the only thing a user runs. It in turn:

- Forks `exec_env_stub` (the binary in `cmd/exec_env_stub/`) several times — one per namespace layer per build.
- Forks Debian tooling like `dpkg-buildpackage`, `tar`, `xz` — through the `container` package (and via `os/exec` in `stream`).
- Talks to Debian mirrors via plain `net/http`.

Nothing else runs. There is no daemon, no service, no persistent worker pool.

## Where state lives

| Kind | Location |
|------|----------|
| Source code, build configuration | conf-dir (any path; `tests/full_build_test.sh` uses a tmpdir) |
| Cached blobs and identity map | object store at `~/.cache/gl-ng` (overridable) |
| Per-session HTTP fetch cache | in-memory in `gl`, keyed by `--cookie` |
| Live build progress | in-memory in `gl`'s `taskui`; serialised to a file at end of run |
| Logs | streamed to stderr via `log` package; per-task buffers in `taskui` |

There are no databases, no on-disk indices besides the object store, and no global config files.

## Phase 1 boundaries

Some things you might expect that are *not* in the repo:

- **No Phase 2 git automation.** Importing does not create branches; lockfiles do not auto-refresh.
- **No image-format conversion.** Phase 1 produces `.tar.gz` only.
- **No flavour system.** One rootfs config per conf-dir.
- **No remote build worker.** Everything runs locally.

These are blueprint Phase 2/3 features. The code is structured so they fit cleanly when added (e.g. ExecEnv abstracts over local-vs-remote, the artifact graph supports multiple top-level targets).

## Files outside the source tree referenced by code

A handful of host-system files are read at runtime:

- `/etc/subuid`, `/etc/subgid` — user namespace ID range entitlements.
- `/usr/bin/newuidmap`, `/usr/bin/newgidmap` — setuid helpers for ID mapping.
- `/usr/share/keyrings/debian-archive-keyring.gpg` — default GPG keyring (overridable via `--keyring`).
- `/usr/bin/gpgv`, `/usr/bin/xz`, `/usr/bin/gzip`, `/usr/bin/bzip2`, `/usr/bin/zstd`, `/usr/bin/tar`, `/usr/bin/patch` — used as subprocesses by the stream and importer packages.

If any of these is missing, the corresponding code path errors out. There is no runtime fallback.
