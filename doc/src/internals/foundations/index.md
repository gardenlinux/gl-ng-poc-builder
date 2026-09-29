# Foundations

The `internal/objstore`, `internal/dirhash`, `internal/stream`, `internal/log`, and `internal/taskui` packages have no domain knowledge — they implement primitives that everything else builds on.

| Package | Domain | Depends on |
|---------|--------|-----------|
| [`objstore`](./objstore.md) | Content-addressed blobs + identity map | std lib |
| [`dirhash`](./dirhash.md) | Deterministic directory hashing | `os.OpenRoot` (Go 1.24+) |
| [`stream`](./stream.md) | Subprocess-based decompression, tar, GPG | `os/exec`, host tools |
| [`log`](./log.md) | Structured logging with multiple targets | std lib |
| [`taskui`](./taskui.md) | Interactive task progress UI | terminal raw mode |

Each is independently testable. The Debian-format and build-system layers compose all five.
