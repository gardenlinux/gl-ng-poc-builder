# `log` — Structured Logging with Multiple Targets

Component-tagged log records, multiple sinks, context-based wiring. ~500 lines of source across 6 files.

```
internal/log/
├── log.go         # Logger, Level, Component, Target interface
├── console.go     # ConsoleTarget — stderr/stdout with ANSI colors
├── buffer.go      # BufferTarget — in-memory ring with cond.Broadcast
├── printer.go     # LogPrinter — goroutine that drains a BufferTarget to stdio
├── exec.go        # NewExecWriters — pipe FDs that emit subprocess output as records
└── serialize.go   # JSON marshal/unmarshal of a BufferTarget
```

## Levels and components

```go
type Level int
const ( Debug Level = iota; Info; Warn; Error )

type Component int
const (
    Engine Component = iota
    Build
    Container
    MountNSC      // mount-namespace
    UserNS
    Deps
    Lockfile
    Importer
    Fetch
    Rootfs
    Binary
    InstallCheck
    Chroot
    Exec
)
```

Both have stable `String()` methods, and both implement `MarshalJSON`/`UnmarshalJSON` that round-trip through those string names — so JSON-serialized records are human-readable (`"info"`, `"build"`) rather than ints. Adding a new component means appending a constant, a `String()` arm, and an entry in `componentByName` (the reverse-lookup map used by `UnmarshalJSON`). Keeping the JSON form symbolic means renumbering the enum is safe — only the string mapping is wire-visible.

## Record and Target

```go
type Record struct {
    Level     Level
    Component Component
    Msg       string
}

type Target interface {
    Emit(Record)
}

var Discard Target = discardTarget{}   // no-op
```

A `Target` is anything with an `Emit(Record)` method. Multiple targets are not natively combined — there's no fanout or tee target. If a component needs both buffering *and* console output, a higher-level wrapper builds a fan-out by hand.

## Logger

```go
type Logger struct { ... }
func (l *Logger) Debug(msg string, args ...any)
func (l *Logger) Info (msg string, args ...any)
func (l *Logger) Warn (msg string, args ...any)
func (l *Logger) Error(msg string, args ...any)
```

A logger carries a single component and a single target. `args` are printf-style — when present, the message is formatted via `fmt.Sprintf`. Otherwise the message is used verbatim.

`emit` is no-op when the receiver or target is nil. This is so that `From(nil, X)` returns a usable logger that drops everything — callers don't have to check.

## Context wiring

```go
func WithTarget(ctx context.Context, t Target) context.Context
func From(ctx context.Context, c Component) *Logger
```

The convention everywhere in the codebase: somewhere near the top of a request lifecycle, a target is installed in the context (`ctx = log.WithTarget(ctx, target)`). Inside any function that wants to log, `l := log.From(ctx, log.Build)` retrieves a logger tagged with that component.

This means individual packages don't need to thread a `*Logger` through every function — they just need a `context.Context`, which they typically have anyway.

## Targets

### ConsoleTarget

```go
type ConsoleTarget struct { ... }
func NewConsoleTarget() *ConsoleTarget
func (c *ConsoleTarget) Emit(r Record)
```

Writes one line per record. Format: `<elapsed> [<LEVEL>] <component>: <msg>`.

- Info → stdout.
- Debug, Warn, Error → stderr.
- ANSI color (red for error, yellow for warn, dim for debug) is applied only when stderr is a tty (`term.IsTerminal`).
- All output is mutex-serialized — concurrent emitters never interleave.
- `<elapsed>` is `time.Since(startTime)` truncated to ms. `startTime` is set at package init.

### BufferTarget

```go
type BufferTarget struct { ... }
func NewBufferTarget() *BufferTarget
func (bt *BufferTarget) Emit(r Record)
func (bt *BufferTarget) Len() int
func (bt *BufferTarget) Reader() *BufferReader

type BufferReader struct { ... }
func (r *BufferReader) Read() (Record, error)  // ErrNoMore at end
```

In-memory append-only buffer. Each emit grows the slice and broadcasts to a `sync.Cond` so that any blocked readers wake up. Multiple `BufferReader`s can read independently — each tracks its own position.

`Read` returns `ErrNoMore` when the reader is caught up. `ErrNoMore` is not a permanent state — more records may arrive later. The taskui package uses this pattern: spawn a printer goroutine per task buffer, when the buffer hits ErrNoMore, the printer waits on the cond.

### LogPrinter

```go
type LogPrinter struct { ... }
func NewLogPrinter(buf *BufferTarget) *LogPrinter
func (p *LogPrinter) Run()      // start goroutine
func (p *LogPrinter) Stop()     // signal + wait for drain
```

Pulls records out of a `BufferTarget` and writes them to stdio with the same formatting as `ConsoleTarget`. Used by the `--view-logs` CLI mode in `gl build` to replay a captured task buffer to the terminal after the run completes.

`Stop` is idempotent (`sync.Once`). It sets `stopFlag`, broadcasts on the buffer's cond to wake the goroutine, and waits on `done`. Once `stopFlag` is set, the goroutine drains remaining records then exits.

## Exec output capture

```go
func NewExecWriters(ctx context.Context, c Component) (stdout *os.File, stderr *os.File, closeFn func())
```

Returns two pipe write-ends suitable for passing as `ExecRequest.Stdout/Stderr` FDs. Each pipe is read by a goroutine that calls `bufio.Scanner.Scan()` — every line of output becomes one log record:

- Subprocess stdout → `Info` level
- Subprocess stderr → `Warn` level

Both records are tagged with the supplied component.

Usage pattern in callers:

```go
out, errf, closeFn := log.NewExecWriters(ctx, log.Build)
req := &ipc.ExecRequest{ ..., Stdout: out, Stderr: errf }
// after start, close write ends so the readers see EOF
out.Close()
errf.Close()
// wait for goroutines to drain
defer closeFn()
```

The `closeFn` waits on a `sync.WaitGroup` of size 2 — both reader goroutines must exit before the function returns. Without this, log records can be lost when the process exits before the scanner has consumed them.

## Serialization

```go
func (bt *BufferTarget) MarshalJSON()           ([]byte, error)
func (bt *BufferTarget) UnmarshalJSON(data []byte) error
func (bt *BufferTarget) WriteTo(w io.Writer)   (int64, error)
func (bt *BufferTarget) ReadFrom(r io.Reader)  (int64, error)
```

JSON encoding is one record per array element with three fields: `level`, `component`, `msg`. Levels and components serialize as **strings** (`"debug"`, `"info"`, `"warn"`, `"error"` and `"engine"`, `"build"`, `"container"`, ...) via their `MarshalJSON` methods, so a captured log file is grep-friendly and the wire format doesn't depend on enum ordering.

Used by taskui to persist per-task log buffers to disk after a build finishes, so `gl build --view-logs` can replay them.

## Tests

`log_test.go` — Logger basics, level emission, format args, nil safety. `buffer_test.go` — emit/read sequence, ErrNoMore, multiple readers, concurrent writers. (Console, exec, printer, serialize are exercised through higher-level taskui and CLI paths.)

## Gotchas

- **`Discard` and `nil` target are not the same.** `Discard.Emit` is a no-op method call; a `nil` target inside a Logger short-circuits earlier. Both are safe.
- **`log.From(nil, ...)` works.** It returns a logger whose target is nil. Useful for tests and for code paths that may run before a target is wired.
- **The `BufferTarget` never trims.** It grows unboundedly. This is fine — buffers are scoped to a single task lifetime, then either serialized or discarded.
- **`NewExecWriters` write-ends should be closed by the *caller* after the child process starts.** If you forget, the readers won't see EOF and `closeFn` will block forever. The IPC layer's stub closes its inherited FDs after `cmd.Start()` for this reason.
