# gl-ng

A from-source, reproducible, image-centric Linux build system for GardenLinux.
It builds Debian packages from source inside isolated user-namespace containers,
assembles them into immutable root filesystems, and keeps full traceability from
the final artifact back to its version-controlled inputs.

This repository (`gl-ng-poc-builder`) holds the build system itself: the `gl`
CLI and the Go packages behind it. The package sources and generated lockfiles
that `gl` consumes live in a separate staging repository.

## Development container

The `.devcontainer/` here is deliberately minimal: a Debian base with the Go
toolchain and the Debian build/stream tooling, running as an unprivileged `dev`
user. Open the folder in a Dev Containers-capable editor, or build the image
directly with `podman build .devcontainer` / `docker build .devcontainer`.

## Building

```sh
make        # fmt, vet, build the gl binary into bin/
make test   # run the unit tests
```

`make` is the single orchestration entry point; read the Makefile before
reaching for any other build command.

## Host requirements

- Go 1.25 or newer.
- Debian build tooling: `build-essential`, `dpkg-dev`, `fakeroot`, `debhelper`.
- Stream tools: `xz-utils`, `gzip`, `bzip2`, `zstd`, `tar`, `gpgv`.
- User-namespace tooling: the `uidmap` package (`newuidmap` / `newgidmap`), plus
  configured `/etc/subuid` and `/etc/subgid` ranges for your user.
