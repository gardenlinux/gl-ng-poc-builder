# Introduction

`gl-ng` is the next-generation build system for [GardenLinux](https://gardenlinux.io). It produces fully reproducible, source-traceable Linux system images by building Debian source packages from scratch inside hermetic containers and assembling them into immutable rootfs images.

This book is the canonical, human-facing manual for the project. It is split into three parts, addressed to three different audiences:

| Part | Audience | What it answers |
|------|----------|-----------------|
| [Concepts](./concepts/overview.md) | Anyone evaluating or starting to use `gl-ng` | *What* the system is and *why* it is built that way |
| [User Guide](./guide/getting-started.md) | People running builds, importing packages, writing `build.yml` | *How* to use the `gl` CLI to do real work |
| [Internals](./internals/overview.md) | Maintainers and contributors to the codebase | *How* the implementation is structured, package by package |

Each part is largely self-contained, but they cross-reference each other heavily. The User Guide does not re-derive theory it expects you to read in the Concepts chapter; the Internals chapter assumes you already know the conceptual model. If something seems to come out of nowhere, follow the link back.

## What `gl-ng` is, in one paragraph

A monorepo of Debian source packages, a Go program (`gl`) that builds them inside a kernel-namespace-based container runtime, and a content-addressed object store that caches every intermediate artifact under a hash of its inputs. Top-level builds — for example, "produce a rootfs that boots and contains `bash` and `coreutils`" — are described as a graph of artifacts whose identities are derived from their content. If two graphs have the same identity, they share the same outputs; if they differ, the difference is traceable to a specific input change.

## What this book is *not*

It is not a set of release notes or a marketing brochure. It is the canonical engineering reference for what `gl-ng` does, why, and how. Where the prose and the code diverge, the code is the source of truth — trust the User Guide and Internals chapters before any other prose. Forward-looking sections (Phase 2 / Phase 3 ideas) are clearly marked; everything else describes the working implementation.

## Status

The project is in **Phase 1**: from-source bootstrap of a minimal rootfs containing `bash` + `coreutils` (and their full transitive runtime closure: `glibc`, `pcre2`, `gmp`, `openssl`, `systemd`, …). Phase 1 explicitly does not include Git-based staging-branch automation, snapshot-based history, or APT-served runtime indexes — those are described conceptually in the [Vision](./concepts/vision.md) chapter and will be filled in once Phase 1 stabilises.
