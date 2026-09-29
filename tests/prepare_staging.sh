#!/usr/bin/env bash
set -euo pipefail

# Prepare a staging/conf directory for gl-ng builds.
#
# This script imports source packages, copies build.yml templates, and generates
# lockfiles. After it completes, the conf directory is ready for `gl build`.
#
# Usage:
#   ./prepare_staging.sh <conf-dir>         # Prepare a specific directory
#   ./prepare_staging.sh                    # Defaults to ../staging (relative to project root)
#
# Required environment:
#   GL_GL_BIN          — path to the `gl` binary (built by `make build`)
#   GL_EXEC_ENV_STUB   — path to the `exec_env_stub` binary (built by `make build`)
#
# Optional environment:
#   GL_CACHE           — object store cache (default: whatever the gl binary uses)
#
# This script does NOT invoke `go build`; the canonical build is `make build`,
# which writes binaries to bin/. Tests and e2e drivers consume those binaries
# via GL_GL_BIN and GL_EXEC_ENV_STUB.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
TEMPLATES_DIR="$SCRIPT_DIR/templates"

CONF_DIR="${1:-$(cd "$PROJECT_DIR/.." && pwd)/staging}"
mkdir -p "$CONF_DIR"
CONF_DIR="$(cd "$CONF_DIR" && pwd)"

PKGS_DIR="$CONF_DIR/pkgs"
mkdir -p "$PKGS_DIR"

GL_BIN="${GL_GL_BIN:-$PROJECT_DIR/bin/gl}"
STUB_BIN="${GL_EXEC_ENV_STUB:-$PROJECT_DIR/bin/exec_env_stub}"

if [ ! -x "$GL_BIN" ] || [ ! -x "$STUB_BIN" ]; then
    echo "ERROR: gl binary or stub not built." >&2
    echo "  GL_BIN=$GL_BIN" >&2
    echo "  STUB_BIN=$STUB_BIN" >&2
    echo "Run \`make build\` from the project root, or set GL_GL_BIN / GL_EXEC_ENV_STUB." >&2
    exit 1
fi

echo "=== gl-ng prepare staging ==="
echo "Project:  $PROJECT_DIR"
echo "Conf dir: $CONF_DIR"
echo "gl bin:   $GL_BIN"
echo "stub:     $STUB_BIN"
echo "Cache:    ${GL_CACHE:-(gl default)}"
echo ""

# --- Build cache flag ---
# Only pass --cache if GL_CACHE is explicitly set; otherwise let the binary use its default.
CACHE_ARGS=()
if [ -n "${GL_CACHE:-}" ]; then
    CACHE_ARGS=(--cache "$GL_CACHE")
fi

# --- InRelease cookie ---
# Generate a unique cookie so all gl invocations in this run share a single InRelease fetch.
COOKIE="$(uuidgen)"
echo "Cookie: $COOKIE"
echo ""

# --- Discover packages from templates ---

PACKAGES=()
for dir in "$TEMPLATES_DIR"/*/; do
    [ -f "$dir/build.yml" ] || continue
    pkg="$(basename "$dir")"
    PACKAGES+=("$pkg")
done

echo "=== Source packages (${#PACKAGES[@]}): ${PACKAGES[*]} ==="
echo ""

# --- Import sources ---

echo "=== Import sources ==="
for pkg in "${PACKAGES[@]}"; do
    if [ -d "$PKGS_DIR/$pkg/src" ]; then
        echo "$pkg: already imported"
        continue
    fi
    echo "Importing $pkg..."
    "$GL_BIN" import \
        "${CACHE_ARGS[@]}" \
        --cookie "$COOKIE" \
        --output "$CONF_DIR" \
        --no-verify \
        "$pkg" || { echo "FAIL: import $pkg"; exit 1; }
done
echo ""

# --- Apply template patches ---
#
# A template may include a `patches/` subdir of unified diffs. Each .patch is
# applied with `patch -p1` from the package source root, in lexical order.
# Stamp file `.gl-patches-applied` prevents double-application on re-runs
# (mirrors the "src/ already imported" short-circuit above).

echo "=== Apply template patches ==="
for pkg in "${PACKAGES[@]}"; do
    patches_dir="$TEMPLATES_DIR/$pkg/patches"
    [ -d "$patches_dir" ] || continue
    src_dir="$PKGS_DIR/$pkg/src"
    stamp="$PKGS_DIR/$pkg/.gl-patches-applied"
    if [ -f "$stamp" ]; then
        echo "$pkg: patches already applied"
        continue
    fi
    shopt -s nullglob
    patches=("$patches_dir"/*.patch)
    shopt -u nullglob
    [ ${#patches[@]} -gt 0 ] || continue
    IFS=$'\n' patches=($(printf '%s\n' "${patches[@]}" | sort))
    unset IFS
    for p in "${patches[@]}"; do
        echo "$pkg: applying $(basename "$p")"
        patch -p1 -d "$src_dir" --no-backup-if-mismatch < "$p" \
            || { echo "FAIL: patch $p"; exit 1; }
    done
    touch "$stamp"
done
echo ""

# --- Copy templates ---

echo "=== Copy build.yml templates ==="
for pkg in "${PACKAGES[@]}"; do
    if [ -f "$TEMPLATES_DIR/$pkg/build.yml" ]; then
        cp "$TEMPLATES_DIR/$pkg/build.yml" "$PKGS_DIR/$pkg/build.yml"
        echo "$pkg: build.yml copied"
    fi
done

if [ -f "$TEMPLATES_DIR/rootfs.yml" ]; then
    cp "$TEMPLATES_DIR/rootfs.yml" "$CONF_DIR/rootfs.yml"
    echo "rootfs.yml copied"
fi
echo ""

# --- Generate lockfiles ---

echo "=== Generate lockfiles ==="
for pkg in "${PACKAGES[@]}"; do
    if [ -f "$PKGS_DIR/$pkg/build-deps.yml" ]; then
        echo "$pkg: lockfile exists"
        continue
    fi
    echo "Generating lockfile for $pkg..."
    "$GL_BIN" lockfile \
        "${CACHE_ARGS[@]}" \
        --cookie "$COOKIE" \
        --output "$CONF_DIR" \
        "$pkg" || { echo "FAIL: lockfile $pkg"; exit 1; }
done
echo ""

# Generate rootfs lockfile
if [ -f "$CONF_DIR/rootfs-deps.yml" ]; then
    echo "rootfs lockfile exists"
else
    echo "Generating rootfs lockfile..."
    "$GL_BIN" lockfile-rootfs \
        "${CACHE_ARGS[@]}" \
        --cookie "$COOKIE" \
        --output "$CONF_DIR" || { echo "FAIL: lockfile-rootfs"; exit 1; }
fi
echo ""

echo "=== Staging prepared ==="
echo "Conf dir: $CONF_DIR"
echo ""
echo "Run builds with:"
echo "  $GL_BIN build --conf-dir $CONF_DIR --stub $STUB_BIN"
