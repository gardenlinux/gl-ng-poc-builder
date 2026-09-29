# Environment Propagation

**`ExecRequest.Env` is an *overlay* on top of inherited environment, not a replacement. Set `ResetEnv: true` only when entering a fresh universe (e.g. a freshly-pivoted container rootfs).**

## Why

The first cut of `ExecEnv` had `Env` mean *replace*: whatever you set on the request was the complete environment the child saw. This sounds clean and predictable, but in a layered system it forces every caller to think about every variable, not just the one it cares about. To set `DEBIAN_FRONTEND=noninteractive` for an `apt-get install`, the caller had to also remember to pass `PATH=...`, and `HOME=...`, and anything else the inner command might need. The codebase accumulated `Env: []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "DEBIAN_FRONTEND=noninteractive"}` boilerplate at ~10 sites, plus thin `execIn<NS>` wrappers that existed only to inject this minimal env.

Worse, it conflated two distinct intents:

- "I want to **add** `DEBIAN_FRONTEND` to whatever the surrounding environment provides." (overlay)
- "I am about to enter a freshly-pivoted rootfs and the host's `LD_LIBRARY_PATH` / `LANG` / `USER` are nonsense — give me a clean slate." (reset)

Replace semantics could express the second case but made the first case painful. Overlay semantics make both easy and keep them visibly distinct in the request.

## How to apply

When constructing an `ExecRequest`:

1. **Default: list only the keys you want to change.** Inheritance handles everything else. Setting `Env: []string{"DEBIAN_FRONTEND=noninteractive"}` adds (or overrides) just that one key on top of whatever the surrounding `ExecEnv` provides.
2. **`ResetEnv: true` resets the inherited base to a minimal `PATH` (`ipc.DefaultPATH`) before applying the overlay.** Use it when the inherited environment would be actively wrong — typically when launching the stub for a fresh `Container` whose root has just been pivoted. Inside that container, child execs leave `Env: nil, ResetEnv: false` to inherit the now-clean base.
3. **The merge happens via `ipc.ResolveEnv(reset, overlay, inherited)`** in `internal/ipc/env.go`. Both `BaseExecEnv` and the stub use the same helper; the stub does not need to import `internal/container`.
4. **On the wire, `ipc.ExecPayload` carries `ResetEnv bool`.** Layer implementations (UserNS, MountNS) inherit by default; only `Container` passes `ResetEnv: true` when launching its stub.

## A common case

Inside a built container running `gl exec-chroot`, the idiomatic shape is:

```go
env.Exec(ctx, ExecRequest{
    Argv: []string{"/bin/bash"},
    Env:  []string{"HOME=/root", "TERM=" + os.Getenv("TERM")},
    // ResetEnv: false — inherit the minimal env the stub already has
})
```

`PATH` comes from the stub's reset env; `HOME` and `TERM` are overlaid; nothing else leaks in from the host.

## See also

- [`internal/ipc`](../internals/runtime/ipc.md) — `ResolveEnv` and `ExecPayload.ResetEnv`.
- [ExecEnv Discipline](./execenv-discipline.md) — the broader rule that all execution flows through this interface.
