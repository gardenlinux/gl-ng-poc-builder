# Summary

[Introduction](./introduction.md)

# Concepts

- [What gl-ng Is](./concepts/overview.md)
- [Vision and Design Principles](./concepts/vision.md)
- [The Artifact Model](./concepts/artifacts.md)
  - [Identity, Inputs, and Caching](./concepts/identity.md)
  - [Depends, Includes, and the Graph](./concepts/graph.md)
- [The Object Store](./concepts/object-store.md)
- [Debian Source and Binary Artifacts](./concepts/debian-artifacts.md)
- [Lockfiles and Build-Time Tooling](./concepts/lockfiles.md)
- [Rootfs Assembly](./concepts/rootfs.md)
- [The Derived Package Index](./concepts/package-index.md)
- [Build Isolation Runtime](./concepts/isolation.md)
- [Source Management and the Staging Repo](./concepts/sources.md)
- [End-to-End Pipeline](./concepts/pipeline.md)

# User Guide

- [Getting Started](./guide/getting-started.md)
- [Host Requirements](./guide/host-requirements.md)
- [The `conf-dir` Layout](./guide/conf-dir.md)
- [Importing Sources](./guide/import.md)
- [Generating Lockfiles](./guide/lockfile.md)
- [Writing `build.yml`](./guide/build-yml.md)
- [Writing `rootfs.yml`](./guide/rootfs-yml.md)
- [Building](./guide/build.md)
- [Inspecting and Debugging Builds](./guide/inspect.md)
- [Running Commands in a Built Rootfs](./guide/exec-chroot.md)
- [Managing the Cache](./guide/cache.md)
- [Resolving Dependencies (debugging)](./guide/resolve.md)
- [End-to-End Walkthrough](./guide/e2e.md)
- [Environment Variables Reference](./guide/env.md)

# Maintainer / Internals

- [How to Read This Chapter](./internals/overview.md)
- [Repository Layout](./internals/layout.md)
- [Foundations](./internals/foundations/index.md)
  - [`objstore` — Content-Addressed Store](./internals/foundations/objstore.md)
  - [`ociclient` — OCI Registry Client & Pull-Through Backend](./internals/foundations/ociclient.md)
  - [`dirhash` — Deterministic Directory Hashing](./internals/foundations/dirhash.md)
  - [`stream` — Stream Processing Primitives](./internals/foundations/stream.md)
  - [`log` — Structured Logging](./internals/foundations/log.md)
  - [`taskui` — Progress UI](./internals/foundations/taskui.md)
- [Debian Format Layer](./internals/debian/index.md)
  - [`debian/deb822`](./internals/debian/deb822.md)
  - [`debian/depends`](./internals/debian/depends.md)
  - [`debian/version`](./internals/debian/version.md)
  - [`debian/index`](./internals/debian/pkgindex.md)
  - [`debian/aptrepo`](./internals/debian/aptrepo.md)
  - [`resolver`](./internals/debian/resolver.md)
- [Build Isolation Runtime](./internals/runtime/index.md)
  - [`ipc` — Parent ↔ Stub Protocol](./internals/runtime/ipc.md)
  - [`container` — ExecEnv Layers](./internals/runtime/container.md)
  - [`cmd/exec_env_stub` — The Stub Binary](./internals/runtime/stub.md)
- [Build System](./internals/build/index.md)
  - [`artifact` — Graph Engine](./internals/build/artifact.md)
  - [`build` — Concrete Artifact Types](./internals/build/build.md)
  - [`importer` — Source Import](./internals/build/importer.md)
  - [`lockfile` — Lockfile Generation](./internals/build/lockfile.md)
  - [`install` — Bootstrap and dpkg Install](./internals/build/install.md)
- [Command-Line Interface](./internals/cli/index.md)
  - [`cmd/gl` — Subcommand Dispatch](./internals/cli/gl.md)
  - [Demo Programs](./internals/cli/demos.md)
- [Testing](./internals/testing.md)

# Architecture Guidelines

- [Overview](./guidelines/overview.md)
- [The Doc-and-Tests Rule](./guidelines/doc-and-tests-rule.md)
- [Artifact Identity and Caching](./guidelines/identity-and-caching.md)
- [Dependency Locality](./guidelines/dependency-locality.md)
- [ExecEnv Discipline](./guidelines/execenv-discipline.md)
- [Environment Propagation](./guidelines/env-propagation.md)
- [Branch and Staging Model](./guidelines/branch-and-staging-model.md)

---

[Glossary](./glossary.md)
