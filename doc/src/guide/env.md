# Environment Variables Reference

Quick reference for environment variables `gl` and the test scripts respect.

| Variable | Used by | Effect |
|----------|---------|--------|
| `GL_CACHE` | `gl exec-chroot`, test scripts | Object store directory. CLI commands also accept `--cache <dir>`. |
| `GL_CACHE_DIR` | `tests/full_build_test.sh` | Cache for the e2e test (default `/tmp/gl-e2e-cache`). |
| `GL_STUB_PATH` | `gl exec-chroot` | Path to `exec_env_stub` binary. Default: alongside the `gl` binary. CLI also accepts `--stub <path>`. |
| `GL_WORK_DIR` | `tests/full_build_test.sh` | Work directory for the e2e test. Default: fresh tmpdir, deleted on exit. |
| `GL_KEEP_WORK` | `tests/full_build_test.sh` | Set to `1` to skip cleanup of work dir on exit. |
| `GL_NO_USERNS` | (reserved) | Skip the user namespace layer. **Not yet implemented end-to-end** in Phase 1; see `AUDIT_REPORT.md`. |

## Defaults

- Object store root, when not overridden, is whatever `objstore.DefaultRoot()` returns — currently `~/.cache/gl-ng` on Linux.
- The stub binary is found by looking next to the `gl` binary itself (i.e. `dirname $(which gl)/exec_env_stub`).

## CLI flags that mirror env vars

Most CLI subcommands accept `--cache <dir>`, which takes precedence over `GL_CACHE`. The same is true for `--stub <path>` vs `GL_STUB_PATH`.

## Variables NOT used (but you might expect)

- `GL_DIST` / `GL_REPO` — not implemented. Use `--dist` / `--repo` flags on `gl import`, `gl lockfile`, `gl resolve`.
- `GL_ARCH` — not implemented. Use `--arch`.
- `DEB_BUILD_OPTIONS` / `DEB_BUILD_PROFILES` from the host — *not* propagated. The build chroot's environment is built from `build.yml`'s `build_profiles:`, `build_options:`, and `extra_build_env:` only.

This isolation is intentional: `gl build` should produce the same output regardless of the surrounding shell.

## Inside the build chroot

For reference, the env vars `gl-ng` sets inside the build chroot before invoking `dpkg-buildpackage`:

| Variable | Source |
|----------|--------|
| `DEB_BUILD_OPTIONS` | From `build_options:` in `build.yml`, joined with spaces |
| `DEB_BUILD_PROFILES` | From `build_profiles:` in `build.yml`, joined with spaces |
| `PATH`, `HOME`, `USER`, `LOGNAME` | Set to default chroot values |
| Each entry in `extra_build_env:` | Verbatim |

`FORCE_UNSAFE_CONFIGURE`, root-as-builder hacks, and other historical workarounds are explicitly *not* set — see decision log in CLAUDE.md (`2026-05-11`).

## Inside `gl exec-chroot`

The container layer sets:

- `PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin`
- `HOME=/root`
- `TERM=xterm`

Plus `GL_ROOTFS=<path>` when `--explore` is used.
