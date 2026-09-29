# Artifact Identity and Caching

**An artifact's identity hash is the cache key. Same inputs must always produce the same identity; different inputs must always produce a different identity. This invariant is the single most important property of the system.**

## Why

The whole build system rests on content-addressed caching. When a build runs, the artifact graph computes each artifact's identity from its declared inputs; if a blob is already in the object store under that identity, the artifact is satisfied without rebuilding. Get this wrong in either direction and a class of subtle bugs follows:

- **Identity too narrow** (different inputs collapse to the same hash): the cache returns stale outputs. The most damaging bug class — silent wrong builds that nothing flags. A real instance was non-deterministic Go map iteration leaking into stanza key order in a generated lockfile, producing two identities for what should have been one cached blob; the inverse can also happen, where a meaningful input is dropped from the identity computation and rebuilds quietly use the wrong cached output.
- **Identity too wide** (same logical inputs produce different hashes): the cache misses, builds run that should have been satisfied. The cost is wasted CPU rather than wrong output, but in a system where a full rebuild can take an hour, the user-experience hit is severe.

## How to apply

When introducing a new artifact type, or modifying an existing one's input set:

1. **Be explicit about what is in identity.** Every input that affects output must be in the identity computation. Every input that does not must not be.
2. **Hash deterministically.** Sort map keys. Walk directories with `os.OpenRoot` (Go 1.24+) so symlinks cannot escape and entry order is stable. Write deb822 stanzas in sorted key order. Do not let `range` over a Go map leak into a hash.
3. **Use `ConcatHash` when composing identities from multiple sub-hashes.** Each input string is independently SHA-256'd to 32 bytes, the digests are concatenated, then hashed again. The implementation is in [`internal/objstore/hash.go`](../internals/foundations/objstore.md); the formula is in [Identity, Inputs, and Caching](../concepts/identity.md).
4. **When debugging a cache miss or a stale cache hit, suspect the identity first.** Run with `--no-cache` and compare. If two identities differ that should match, find the input that varies between runs.

When changing what counts as an input — for example, adding `extra_build_env` to an artifact's identity — recognize that you are invalidating every existing cache entry for that artifact type. Do this deliberately, not as a side-effect.

## See also

- [Identity, Inputs, and Caching](../concepts/identity.md) — the formal model.
- [`internal/objstore`](../internals/foundations/objstore.md) — Hash type and ConcatHash implementation.
- [Dependency Locality](./dependency-locality.md) — a related correctness rule that builds *on top of* identity correctness.
