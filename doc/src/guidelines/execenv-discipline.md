# ExecEnv Discipline

**Every subprocess in the build system runs through the `ExecEnv` interface. No direct `os/exec` outside `BaseExecEnv`. No Docker, Podman, or other third-party container runtimes — build isolation is kernel-native.**

## Why

The build system is structured as a stack of nestable execution environments: the host (`BaseExecEnv`) sits at the bottom, and `UserNS`, `MountNS`, and `Container` layer on top by spawning a stub child in a new namespace and exposing the same interface. A consumer asks for an `ExecEnv` and runs commands through it without knowing — or caring — which layer it actually lands on.

Two properties depend on every command going through this interface:

- **Backend substitutability.** A future VM or remote-runner backend would replace `BaseExecEnv` with something that ships requests over a wire, but the artifact code calling `env.Exec(...)` does not change. The moment one place in the codebase shells out directly with `os/exec`, that place becomes incompatible with any backend other than the local host. The constraint exists not because direct exec is unsafe in itself, but because it forecloses substitution.
- **Isolation correctness.** The container layer is responsible for setting up user namespaces, mount namespaces, PID namespaces, and `pivot_root`. A direct `os/exec` invocation skips that setup, runs in the parent's namespaces, and silently violates whatever isolation the surrounding artifact thought it had. The fix for "I just need to run one quick `tar`" is never to bypass the layer — it is to call `Exec` on the right `ExecEnv`.

A third reason, less load-bearing but real: every `ExecEnv` instance can hold context (root path, env defaults, FD wiring) that direct `os/exec` calls reinvent ad-hoc. Routing through the interface keeps that context in one place.

## How to apply

When writing code that needs to run a subprocess:

1. **Take an `ExecEnv` parameter.** Don't import `os/exec`. The `Artifact.Build` method receives a build context that exposes the right environment; pass it down.
2. **Use `ExecRequest`'s structured fields.** Argv, env (overlay — see [Environment Propagation](./env-propagation.md)), working directory, FD passing, stdin/stdout wiring all flow through the request. Don't reach around the interface to set up FDs or env vars.
3. **The stream-processing primitives are the documented exception.** `internal/stream` spawns `xz`, `gzip`, `tar`, `gpgv` directly via `os/exec`. This is acknowledged technical debt from Phase 1 — those primitives are pure I/O, run on the host, and don't carry isolation guarantees. Do not extend the exception. Anything above `stream` (artifact builds, chroot assembly, source extraction inside builds) goes through `ExecEnv`.
4. **Don't introduce third-party container runtimes.** No Docker, no Podman, no `nsenter`-shelling, no `unshare(1)` invocations. The container layer's namespaces are constructed in-process from the stub child; that is the only path.

## What this excludes

- `os/exec` calls in artifact code, importer code, lockfile generation, CLI subcommands.
- Wrapper functions that take an `ExecEnv` but secretly call `os/exec` for "just this one command".
- Calling `docker run`, `podman run`, `buildah`, or any external container CLI.
- Direct `clone(2)` / `unshare(2)` syscalls outside the `container` package.

## See also

- [`internal/container`](../internals/runtime/container.md) — `ExecEnv` interface and the layer implementations.
- [Environment Propagation](./env-propagation.md) — how env flows through `ExecRequest`, which is the most common reason people are tempted to reach around the interface.
- [Build Isolation Runtime](../concepts/isolation.md) — the conceptual model that this discipline preserves.
