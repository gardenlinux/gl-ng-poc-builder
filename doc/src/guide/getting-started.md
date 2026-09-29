# Getting Started

This page sets up the minimum needed to run `gl` and verify it works. The fastest path from "freshly cloned repo" to "rootfs that boots in `exec-chroot`" is the e2e test script — that path is in [End-to-End Walkthrough](./e2e.md). This page covers the manual setup so you understand what the script automates.

## Prerequisites

Phase 1 targets Linux with unprivileged user namespaces enabled. The full list of host packages, Go version, and `/etc/subuid` / `/etc/subgid` configuration lives in [Host Requirements](./host-requirements.md). Read that page first; the rest of the guide assumes everything in it has been satisfied.

## Building the binaries

From the repo root:

```bash
cd /path/to/gl-ng
make build
```

This produces `bin/gl` and `bin/exec_env_stub`. The Makefile is the canonical build entry point; nothing in the repo (tests, scripts, docs) ever calls `go build` directly.

Commands that need the stub will accept `--stub <path>` or read `GL_STUB_PATH` from the environment. `make test` and `make e2e` set `GL_EXEC_ENV_STUB` for you.

## A first sanity check

```bash
./bin/gl status
```

This prints which components are linked into the binary. If it errors, your build is broken before you get to anything else.

## The conf-dir, briefly

`gl` works against a *conf-dir* — a directory with this layout:

```
<conf-dir>/
├── pkgs/
│   ├── <pkgname>/
│   │   ├── sources.yml      # written by gl import
│   │   ├── src/             # source tree (written by gl import)
│   │   ├── build.yml        # hand-edited
│   │   └── build-deps.yml   # written by gl lockfile
│   └── ...
├── rootfs.yml               # hand-edited
└── rootfs-deps.yml          # written by gl lockfile-rootfs
```

The conf-dir holds everything that is *specific to this set of packages and this rootfs*. The object store holds everything that is content-addressed and can be GC'd: source tarballs, lockfile blobs, build outputs.

See [The conf-dir Layout](./conf-dir.md) for full detail.

## Where the cache lives

By default, the object store is at `~/.cache/gl-ng`. Override with:

- `--cache <dir>` on individual `gl` commands, or
- `GL_CACHE=<dir>` in the environment (read by `exec-chroot` and the test scripts).

## What's next

- [The conf-dir layout](./conf-dir.md) — the directory you build against.
- [Importing Sources](./import.md) — `gl import`.
- [Generating Lockfiles](./lockfile.md) — `gl lockfile` and `gl lockfile-rootfs`.
- [Writing build.yml](./build-yml.md) — the per-package configuration.
- [Building](./build.md) — `gl build`.
- [End-to-End Walkthrough](./e2e.md) — running the e2e test, which automates the whole pipeline.
