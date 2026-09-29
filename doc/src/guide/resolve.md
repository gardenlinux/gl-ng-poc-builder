# Resolving Dependencies (debugging)

`gl resolve` is a side door into the dependency resolver, useful for diagnosing why a particular package set is unsatisfiable, for inspecting the closure of an arbitrary set of packages, or for feeding the closure into other tools that want a parseable list of binaries (or source-binary pairs).

## Usage

```text
gl resolve [flags] <pkg1> [pkg2] ...

Flags:
  --index string      Packages index blob hash to resolve against
  --repo string       APT repository URL (used if --index not given)
  --dist string       Distribution
  --arch string       Architecture
  --cache string      Object store directory
  --cookie string     InRelease cache cookie (only with --repo)
  --source            Prefix each output line with <source>:
```

If `--index` is given, the resolver runs against that blob (typically a lockfile blob from the cache). If not, it fetches `Packages.gz` from the configured mirror — going through the same `aptrepo.FetchInRelease` path the importer and lockfile generator use, so `--cookie` shares the InRelease blob with adjacent invocations.

`gl resolve` always prints the full closure (one line per package). All log output goes to stderr; only the resolution result lands on stdout. Pipe stdout into `awk`, `grep`, or `sort`; pipe stderr to `/dev/null` if you don't want to see progress.

## Output format

```text
<binary-name> <version>
```

With `--source`:

```text
<source-name>:<binary-name> <version>
```

The source name is taken from the binary's `Source:` stanza field, with a `(version)` suffix stripped if present. When the field is absent, the source name equals the binary name (per Debian policy).

## Example: ad-hoc closure

```bash
gl resolve --repo https://deb.debian.org/debian --dist testing bash coreutils libc6
```

stdout:

```
bash 5.2.37-2
coreutils 9.5-1
libc6 2.40-7
...
```

stderr:

```
3ms [INFO] lockfile: downloading https://deb.debian.org/debian/dists/testing/InRelease
175ms [INFO] lockfile: downloading https://deb.debian.org/debian/dists/testing/main/binary-amd64/Packages.gz
1.04s [INFO] deps: resolving 3 package(s) against index (78234 entries)
1.298s [INFO] deps: resolved 47 packages
```

## Example: source mapping

```bash
gl resolve --repo https://deb.debian.org/debian --dist testing --source libgcc-s1 \
  | awk -F: '{print $1}' | sort -u
```

Produces the source-package name(s) that ship `libgcc-s1` and its closure — convenient for "which sources do I need to import to mirror this set?". The Phase-2 snapshot replay script (`hack/replay_snapshot_into_staging.sh`) uses exactly this pattern.

## Example: against a stored lockfile

```bash
# Find a lockfile blob hash
LOCK_HASH=$(yq '.amd64' staging/pkgs/bash/build-deps.yml)

# Resolve a specific package against it
gl resolve --index "$LOCK_HASH" --arch amd64 gcc
```

This re-resolves `gcc` (and its transitive deps) using the package set frozen in the lockfile, rather than the current mirror state. Useful for "would this currently-failing build work against the version of testing the lockfile was generated for?"

## Common debugging patterns

### "Why is this package being pulled in?"

The resolver doesn't currently print provenance ("X is in the closure because Y depends on it"). The closest workaround is to compare closures with and without a specific seed:

```bash
gl resolve bash > /tmp/with-bash.txt
gl resolve coreutils > /tmp/with-coreutils.txt
diff /tmp/with-bash.txt /tmp/with-coreutils.txt
```

The lines unique to one show what only that package brings in.

### "Why is this unsatisfiable?"

The resolver returns a structured error when no solution exists. The error includes:

- The unsatisfiable requirement.
- The exploration tree the solver walked before giving up.
- The reason each branch was abandoned (conflict, missing provider, etc.).

```bash
gl resolve some-impossible-combo
# resolve failed:
# requirement "foo (>= 2.0)" cannot be satisfied:
#   no provider in index
# ...
```

The full error format is documented in [resolver internals](../internals/debian/resolver.md#error-reporting).

### "Does this lockfile include X?"

```bash
gl resolve --index <lockfile-hash> X 2>/dev/null | grep -q "^X " && echo yes || echo no
```

## Limitations

- **No virtual-package introspection.** `gl resolve foo` works whether `foo` is a real or virtual package, but doesn't tell you which provider it picked except by reading the line that appears in the output.
- **No version constraint parsing on the CLI.** You can resolve `bash`; you can't resolve `bash (>= 5.2)` from the command line. Use the resolver API in tests for that.
- **Architecture is global.** The whole resolution runs against one `--arch`. Cross-arch experiments need separate invocations.

## See also

- [Resolver internals](../internals/debian/resolver.md) — algorithm, error format.
- [Generating Lockfiles](./lockfile.md) — the resolver's primary user.
- [Concept: Lockfiles — Determinism](../concepts/lockfiles.md#determinism) — why the resolver must be reproducible.
