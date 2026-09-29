# Command-Line Interface

The `gl` binary is a thin shell over the build system. It parses subcommands, opens the object store, locates the conf-dir, and hands off to the right `internal/` package. There's no sophisticated CLI framework — `os.Args` and `flag.FlagSet`, nothing else.

```
cmd/
├── gl/                # The user-facing CLI
│   ├── main.go        # Subcommand dispatch
│   ├── build.go       # build subcommand (+ --view-logs replay)
│   ├── graph.go       # graph subcommand
│   ├── cache.go       # cache status / gc / blobs / map
│   ├── import.go      # import
│   ├── commands.go    # lockfile, lockfile-rootfs, status
│   ├── exec_chroot.go # exec-chroot
│   ├── resolve.go     # resolve (debug)
│   └── util.go        # shared helpers (openStore, findConfDir, rootContext)
├── exec_env_stub/     # Namespace setup helper — see runtime/stub.md
├── logdemo/           # Demo of the log package
└── taskdemo/          # Demo of the taskui package
```

## What lives in each subcommand

| Subcommand | File | What it does |
|------------|------|--------------|
| `build` | `build.go::cmdBuild` | Construct the artifact graph and run the engine with TUI |
| `graph` | `graph.go::cmdGraph` | Same graph construction, but print a Mermaid diagram instead of building |
| `cache status/gc/blobs/map` | `cache.go::cmdCache*` | Object-store inspection + garbage collection |
| `import` | `import.go::cmdImport` | Hand off to `internal/importer.Import` |
| `lockfile` | `commands.go::cmdLockfile` | Hand off to `internal/lockfile.Generate` |
| `lockfile-rootfs` | `commands.go::cmdLockfileRootfs` | Hand off to `internal/lockfile.GenerateRootfs` |
| `exec-chroot` | `exec_chroot.go::cmdExecChroot` | Extract a built rootfs and run a command in it |
| `resolve` | `resolve.go::cmdResolve` | Standalone resolver — debug aid |
| `status` | `commands.go::cmdStatus` | One-line cache summary |

## Reading order

1. [`cmd/gl`](./gl.md) — how dispatch works and what each subcommand calls into.
2. [Demo programs](./demos.md) — `logdemo` and `taskdemo`, useful for understanding the foundation packages without dragging in the build system.

The stub binary `cmd/exec_env_stub/` is documented separately under [Build Isolation Runtime → The Stub Binary](../runtime/stub.md) — it's not really a CLI in the user-facing sense, just a process that the container layers re-exec.
