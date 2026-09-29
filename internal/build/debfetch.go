package build

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"

	"gl-ng/internal/debian/index"
	"gl-ng/internal/log"
	"gl-ng/internal/objstore"
)

type DebFetcher struct {
	store   *objstore.Store
	repoURL string
	client  *http.Client
}

func NewDebFetcher(store *objstore.Store, repoURL string) *DebFetcher {
	return &DebFetcher{
		store:   store,
		repoURL: repoURL,
		client:  &http.Client{},
	}
}

func (f *DebFetcher) Fetch(pkg *index.Package) (objstore.Hash, error) {
	if pkg.SHA256 == "" {
		return objstore.Hash{}, fmt.Errorf("package %s has no SHA256", pkg.Name)
	}

	h, err := objstore.NewHash(pkg.SHA256)
	if err != nil {
		return objstore.Hash{}, fmt.Errorf("invalid hash for %s: %w", pkg.Name, err)
	}

	if f.store.Blobs.Has(h) {
		return h, nil
	}

	if pkg.Filename == "" {
		return objstore.Hash{}, fmt.Errorf("package %s has no Filename field", pkg.Name)
	}

	url := fmt.Sprintf("%s/%s", f.repoURL, pkg.Filename)
	resp, err := f.client.Get(url)
	if err != nil {
		return objstore.Hash{}, fmt.Errorf("fetch %s: %w", pkg.Name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return objstore.Hash{}, fmt.Errorf("fetch %s: HTTP %d", pkg.Name, resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return objstore.Hash{}, fmt.Errorf("read %s: %w", pkg.Name, err)
	}

	actualHash := sha256.Sum256(data)
	actualHex := hex.EncodeToString(actualHash[:])
	if actualHex != pkg.SHA256 {
		return objstore.Hash{}, fmt.Errorf("hash mismatch for %s: got %s, want %s", pkg.Name, actualHex, pkg.SHA256)
	}

	storedHash, err := f.store.Blobs.Store(bytes.NewReader(data))

	if err != nil {
		return objstore.Hash{}, fmt.Errorf("store %s: %w", pkg.Name, err)
	}

	return storedHash, nil
}

func (f *DebFetcher) FetchAll(ctx context.Context, packages []*index.Package) error {
	l := log.From(ctx, log.Fetch)
	var wg sync.WaitGroup
	errCh := make(chan error, len(packages))
	sem := make(chan struct{}, 16)

	var fetched atomic.Int32
	var cached atomic.Int32
	total := len(packages)

	for _, pkg := range packages {
		wg.Add(1)
		go func(p *index.Package) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			h, _ := objstore.NewHash(p.SHA256)
			alreadyHas := h.IsZero() == false && f.store.Blobs.Has(h)

			if _, err := f.Fetch(p); err != nil {
				errCh <- fmt.Errorf("%s: %w", p.Name, err)
			} else if alreadyHas {
				n := cached.Add(1)
				_ = n
			} else {
				n := fetched.Add(1)
				if n%10 == 0 || int(n)+int(cached.Load()) == total {
					l.Info("%d/%d downloaded, %d cached", n, total, cached.Load())
				}
			}
		}(pkg)
	}

	wg.Wait()
	close(errCh)

	l.Info("complete: %d fetched, %d cached (of %d total)", fetched.Load(), cached.Load(), total)

	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return fmt.Errorf("failed to fetch %d packages; first: %w", len(errs), errs[0])
	}
	return nil
}
