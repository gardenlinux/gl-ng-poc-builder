# Host Requirements

This page lists everything that must be installed and configured on the build host before `make build`, the test suite, or the end-to-end driver will work. It is the canonical list — the README points here, and the e2e walkthrough assumes it has been satisfied.

## Operating system

Linux with unprivileged user namespaces enabled. Phase 1 targets Debian / Debian-derivative hosts; other distributions can work but are not exercised in CI.

## Tooling

The Go toolchain and a stock Debian package-build chain:

| Tool group              | Packages |
| ----------------------- | --------------------------------------------------------------- |
| Go compiler             | Go ≥ 1.24 (required for `os.OpenRoot`; `go version` to check). |
| Build chain             | `build-essential`, `dpkg-dev`, `fakeroot`, `debhelper`. |
| Stream subprocess pipes | `xz-utils`, `gzip`, `bzip2`, `zstd`, `tar`, `gpgv`. |
| User-namespace helpers  | `uidmap` (provides `newuidmap` and `newgidmap`). |
| Testing utilities       | `coreutils` (almost certainly already present), `gnupg` (full `gpg` — the unit tests in `internal/stream` generate test keys; runtime only needs `gpgv`). |
| Documentation (optional)| `mdbook`, `mdbook-mermaid`. |

On a stock Debian-family system:

```bash
sudo apt-get install -y \
    golang-go build-essential dpkg-dev fakeroot debhelper \
    xz-utils gzip bzip2 zstd tar gpgv gnupg \
    uidmap coreutils
```

For the documentation toolchain (only needed if you want to build or serve `doc/`):

```bash
cargo install mdbook mdbook-mermaid
# or grab pre-built binaries from the upstream releases.
```

## Subordinate UID and GID ranges

`gl` runs every build inside a user namespace and uses subordinate UID/GID ranges to provide a fake root with no real privilege. The host must have entries for your user in `/etc/subuid` and `/etc/subgid`:

```bash
grep "^$(whoami):" /etc/subuid /etc/subgid
```

Both files should print a line of the form `<user>:<start>:<count>`. On most Debian systems the package manager creates these automatically when the user is added. If they are missing:

```bash
sudo usermod --add-subuids 100000-165535 --add-subgids 100000-165535 "$(whoami)"
```

A range of 65536 IDs is enough for one in-flight container; doubling it is harmless.

## Disk and memory

Build chroots use tmpfs by default. Memory needs grow with the largest single build; GCC with high `-j` levels has been observed to consume tens of GB. The reference build VM has 503 GB of RAM and 238 GB of free disk, and treats both as effectively unconstrained.

## Verifying the host

A quick check that the most common requirements are met:

```bash
go version                                   # 1.24 or newer
which dpkg-deb fakeroot newuidmap gpgv tar   # every line should print a path
grep "^$(whoami):" /etc/subuid /etc/subgid   # two lines, non-empty count
```

If any of those print nothing, fix it before running `make`.

## What `make` expects

Once the requirements above are met, the canonical entry point is the project Makefile:

- `make` (or `make build`) builds `bin/gl` and `bin/exec_env_stub`.
- `make test` runs the Go test suite, after building, with `GL_EXEC_ENV_STUB` pointed at `bin/exec_env_stub`.
- `make e2e` runs `tests/full_build_test.sh` against the same binaries.
- `make doc` and `make serve-doc` build / serve this book — only needed if you have the documentation toolchain installed.

The Makefile is the single source of truth for build orchestration; nothing else in the repo invokes `go build` directly.
