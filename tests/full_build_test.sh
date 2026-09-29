#!/usr/bin/env bash
set -euo pipefail

# gl-ng Phase 1 end-to-end integration test.
#
# Usage:
#   ./full_build_test.sh              # Run all phases
#   ./full_build_test.sh import       # Prepare staging only (import + lockfile)
#   ./full_build_test.sh lockfile     # Same as import (prepare does both)
#   ./full_build_test.sh graph        # Prepare + show graph
#   ./full_build_test.sh build        # Prepare + build
#   ./full_build_test.sh verify       # All phases including exec-chroot verification
#
# Required environment (provided by `make e2e`):
#   GL_GL_BIN          — path to the `gl` binary (canonical: $PROJECT/bin/gl)
#   GL_EXEC_ENV_STUB   — path to `exec_env_stub` (canonical: $PROJECT/bin/exec_env_stub)
#
# Optional environment:
#   GL_WORK_DIR    — work directory (default: a fresh `mktemp -d`, removed on exit)
#   GL_KEEP_WORK   — set to 1 to skip cleanup of the work dir on exit
#
# The work dir contains two subdirs:
#   $WORK_DIR/cache    — object store cache for this run
#   $WORK_DIR/staging  — conf dir (imported sources, lockfiles, build.yml)
#
# This script does NOT invoke `go build`; binaries are produced by `make build`
# and consumed via GL_GL_BIN / GL_EXEC_ENV_STUB.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

GL_BIN="${GL_GL_BIN:-$PROJECT_DIR/bin/gl}"
STUB_BIN="${GL_EXEC_ENV_STUB:-$PROJECT_DIR/bin/exec_env_stub}"

if [ ! -x "$GL_BIN" ] || [ ! -x "$STUB_BIN" ]; then
    echo "ERROR: gl binary or stub not built." >&2
    echo "  GL_BIN=$GL_BIN" >&2
    echo "  STUB_BIN=$STUB_BIN" >&2
    echo "Run \`make build\` (or \`make e2e\`, which depends on build)." >&2
    exit 1
fi

PHASE="${1:-all}"

# Work directory layout: $WORK_DIR/cache + $WORK_DIR/staging.
if [ -n "${GL_WORK_DIR:-}" ]; then
    WORK_DIR="$GL_WORK_DIR"
    mkdir -p "$WORK_DIR"
else
    WORK_DIR="$(mktemp -d /tmp/gl-e2e-XXXXXX)"
fi
CACHE_DIR="$WORK_DIR/cache"
STAGING_DIR="$WORK_DIR/staging"
mkdir -p "$CACHE_DIR" "$STAGING_DIR"

cleanup() {
    if [ "${GL_KEEP_WORK:-}" = "1" ]; then
        echo "Keeping work dir: $WORK_DIR"
    else
        echo "Cleaning up $WORK_DIR..."
        rm -rf "$WORK_DIR"
    fi
}
trap cleanup EXIT

echo "=== gl-ng Phase 1 E2E Test ==="
echo "Phase:    $PHASE"
echo "Project:  $PROJECT_DIR"
echo "Work:     $WORK_DIR"
echo "  cache:  $CACHE_DIR"
echo "  staging:$STAGING_DIR"
echo "gl:       $GL_BIN"
echo "stub:     $STUB_BIN"
echo ""

# --- Prepare staging (import + templates + lockfiles) ---

phase_prepare() {
    GL_CACHE="$CACHE_DIR" \
    GL_GL_BIN="$GL_BIN" \
    GL_EXEC_ENV_STUB="$STUB_BIN" \
        "$SCRIPT_DIR/prepare_staging.sh" "$STAGING_DIR"
}

# --- Phase: Graph ---

phase_graph() {
    echo "=== PHASE: Dependency graph ==="

    GRAPH_OUTPUT="$WORK_DIR/full_build_depends.md"

    "$GL_BIN" graph \
        --cache "$CACHE_DIR" \
        --conf-dir "$STAGING_DIR" \
        --stub "$STUB_BIN" \
        --output "$GRAPH_OUTPUT" || { echo "FAIL: graph"; exit 1; }

    echo "Written to: $GRAPH_OUTPUT"
    echo ""
    cat "$GRAPH_OUTPUT"
    echo ""
}

# --- Phase: Build ---

phase_build() {
    echo "=== PHASE: Build artifact graph ==="
    echo "Building..."

    "$GL_BIN" build \
        --cache "$CACHE_DIR" \
        --conf-dir "$STAGING_DIR" \
        --stub "$STUB_BIN" || { echo "FAIL: build"; exit 1; }

    # Re-run cached to capture the rootfs identity from the summary.
    BUILD_SUMMARY=$("$GL_BIN" build \
        --cache "$CACHE_DIR" \
        --conf-dir "$STAGING_DIR" \
        --stub "$STUB_BIN" \
        2>/dev/null)

    ROOTFS_ID=$(echo "$BUILD_SUMMARY" | grep "rootfs identity:" | awk '{print $NF}')
    if [ -z "$ROOTFS_ID" ]; then
        echo "FAIL: Could not determine rootfs identity from build output"
        exit 1
    fi
    echo ""
    echo "Rootfs identity: $ROOTFS_ID"
    echo ""
}

# --- Phase: Verify ---

phase_verify() {
    if [ -z "${ROOTFS_ID:-}" ]; then
        echo "Running build first to get rootfs identity..."
        phase_build
    fi

    echo "=== PHASE: Exec-chroot verification ==="
    EXEC_OUTPUT=$("$GL_BIN" exec-chroot --cache "$CACHE_DIR" "$ROOTFS_ID" bash -c 'echo PHASE1_OK && ls /usr/bin/cat && id' 2>&1) || {
        echo "FAIL: exec-chroot"
        echo "$EXEC_OUTPUT"
        exit 1
    }

    if ! echo "$EXEC_OUTPUT" | grep -q "PHASE1_OK"; then
        echo "FAIL: exec-chroot did not produce expected output"
        echo "$EXEC_OUTPUT"
        exit 1
    fi

    echo "$EXEC_OUTPUT"
    echo ""
}

# --- Dispatch ---

case "$PHASE" in
    import|lockfile)
        phase_prepare
        ;;
    graph)
        phase_prepare
        phase_graph
        ;;
    build)
        phase_prepare
        phase_graph
        phase_build
        ;;
    verify|all)
        phase_prepare
        phase_graph
        phase_build
        phase_verify
        echo "=== ALL PHASES COMPLETE ==="
        echo "Phase 1 validation passed: rootfs built entirely from source."
        ;;
    *)
        echo "Unknown phase: $PHASE"
        echo "Usage: $0 [import|lockfile|graph|build|verify|all]"
        exit 1
        ;;
esac
