package build

// e2e_main_test.go provides shared setup for the build package's heavyweight
// end-to-end tests (TestHelloEndToEnd and the TestSynthetic* family). The
// Debian testing binary index is fetched once per `go test` invocation and
// served to every test that needs it, because re-fetching it per test would
// be wasteful (~30 MB compressed) and re-resolving build-deps for every
// package would put real load on deb.debian.org.
//
// Tests that need this setup must call requireE2E(t) at the top, which
// transparently skips the test under -short, when GL_EXEC_ENV_STUB is unset,
// or when the index fetch failed (no network).

import (
	"context"
	"os"
	"sync"
	"testing"

	"gl-ng/internal/debian/index"
	"gl-ng/internal/lockfile"
	"gl-ng/internal/log"
	"gl-ng/internal/objstore"
)

// e2eState holds the package-level shared resources. All fields are populated
// by ensureE2EState() under sync.Once. setupErr is non-nil if setup decided to
// skip; callers should check it via requireE2E(t).
type e2eState struct {
	once     sync.Once
	store    *objstore.Store
	storeDir string
	idx      *index.Index
	stubPath string
	setupErr error
}

var sharedE2E e2eState

// ensureE2EState performs first-run setup: it picks a temp store dir (or
// honors GL_CACHE_DIR if set so a developer can reuse the cache across runs),
// opens the store, and fetches the Debian testing binary index. Failures are
// recorded in setupErr — they cause the test to skip, not fail.
func ensureE2EState() {
	sharedE2E.once.Do(func() {
		stub := os.Getenv("GL_EXEC_ENV_STUB")
		sharedE2E.stubPath = stub

		dir := os.Getenv("GL_CACHE_DIR")
		if dir == "" {
			d, err := os.MkdirTemp("", "gl-build-e2e-")
			if err != nil {
				sharedE2E.setupErr = err
				return
			}
			dir = d
		} else {
			if err := os.MkdirAll(dir, 0755); err != nil {
				sharedE2E.setupErr = err
				return
			}
		}
		sharedE2E.storeDir = dir

		store, err := objstore.Open(dir)
		if err != nil {
			sharedE2E.setupErr = err
			return
		}
		sharedE2E.store = store

		ctx := log.WithTarget(context.Background(), log.Discard)
		idx, err := lockfile.FetchBinaryIndex(ctx, store, "https://deb.debian.org/debian", "testing", "amd64", "gl-build-e2e")
		if err != nil {
			sharedE2E.setupErr = err
			return
		}
		sharedE2E.idx = idx
	})
}

// requireE2E ensures the shared state is ready and skips the test if it isn't.
// Reasons to skip: -short mode, missing GL_EXEC_ENV_STUB, or failed network
// fetch of the Debian index. Tests that exercise the full container/install
// path also need user namespaces; they should attempt to enter them and skip
// on failure (see install/bootstrap_test.go for the pattern).
func requireE2E(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping e2e build test in -short mode")
	}
	if os.Getenv("GL_EXEC_ENV_STUB") == "" {
		t.Skip("GL_EXEC_ENV_STUB not set; run via `make test`")
	}
	ensureE2EState()
	if sharedE2E.setupErr != nil {
		t.Skipf("e2e setup failed (likely no network): %v", sharedE2E.setupErr)
	}
}

// TestMain is intentionally minimal — setup is lazy via sync.Once so tests
// that don't need the index don't pay for it. The temp store dir is left on
// disk; the OS reclaims /tmp eventually and a developer pointing GL_CACHE_DIR
// at a stable location wants the cache to persist.
func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
