package ociclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"gl-ng/internal/objstore"
)

// ErrNotFound is returned when a blob or manifest is absent (HTTP 404).
var ErrNotFound = errors.New("ociclient: not found")

// Client speaks OCI distribution v2 against a single repository <name> on a
// registry. It uses plain HTTP (loopback test registry) — TLS/auth is deferred
// (oci-cache-design.md §9).
type Client struct {
	base string // e.g. "http://localhost:5000"
	name string // repository name, e.g. "gl-ng"
	http *http.Client

	configPushed bool // empty-config blob pushed this session
}

// New constructs a Client for registry (host[:port], optionally with a path
// prefix stripped) and repository name. registry may be given as
// "localhost:5000" or "localhost:5000/gl-ng" via SplitRef.
func New(registry, name string) *Client {
	base := registry
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	return &Client{base: strings.TrimRight(base, "/"), name: name, http: http.DefaultClient}
}

// SplitRef splits a "host[:port]/repo/name" reference into registry host and
// repository name. If no "/" is present, name is empty.
func SplitRef(ref string) (registry, name string) {
	ref = strings.TrimPrefix(ref, "http://")
	ref = strings.TrimPrefix(ref, "https://")
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

func (c *Client) blobURL(h objstore.Hash) string {
	return fmt.Sprintf("%s/v2/%s/blobs/%s", c.base, c.name, digest(h))
}

func (c *Client) manifestURL(ref string) string {
	return fmt.Sprintf("%s/v2/%s/manifests/%s", c.base, c.name, ref)
}

// Reachable reports whether the registry answers GET /v2/ (cheap health check).
func (c *Client) Reachable() bool {
	resp, err := c.http.Get(c.base + "/v2/")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized
}

// HasBlob reports whether a blob exists (HEAD .../blobs/sha256:<h>).
func (c *Client) HasBlob(h objstore.Hash) (bool, error) {
	resp, err := c.http.Head(c.blobURL(h))
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("ociclient: HEAD blob: unexpected status %d", resp.StatusCode)
	}
}

// PushBlob uploads a blob monolithically: POST to open a session, then PUT the
// content with ?digest=. A blob already present is not re-uploaded.
func (c *Client) PushBlob(h objstore.Hash, r io.Reader, size int64) error {
	has, err := c.HasBlob(h)
	if err != nil {
		return err
	}
	if has {
		if rc, ok := r.(io.Closer); ok {
			rc.Close()
		}
		return nil
	}

	// Open an upload session.
	postURL := fmt.Sprintf("%s/v2/%s/blobs/uploads/", c.base, c.name)
	resp, err := c.http.Post(postURL, "", nil)
	if err != nil {
		return fmt.Errorf("ociclient: open upload: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("ociclient: open upload: status %d", resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	if location == "" {
		return errors.New("ociclient: upload session returned no Location")
	}
	if strings.HasPrefix(location, "/") {
		location = c.base + location
	}

	// Monolithic PUT with the digest query param.
	sep := "?"
	if strings.Contains(location, "?") {
		sep = "&"
	}
	putURL := location + sep + "digest=" + digest(h)
	req, err := http.NewRequest(http.MethodPut, putURL, r)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = size
	putResp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("ociclient: PUT blob: %w", err)
	}
	defer putResp.Body.Close()
	if putResp.StatusCode != http.StatusCreated && putResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(putResp.Body, 512))
		return fmt.Errorf("ociclient: PUT blob: status %d: %s", putResp.StatusCode, body)
	}
	return nil
}

// PullBlob fetches a blob (GET .../blobs/sha256:<h>). ErrNotFound on 404. The
// caller must Close the returned reader.
func (c *Client) PullBlob(h objstore.Hash) (io.ReadCloser, int64, error) {
	resp, err := c.http.Get(c.blobURL(h))
	if err != nil {
		return nil, 0, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return resp.Body, resp.ContentLength, nil
	case http.StatusNotFound:
		resp.Body.Close()
		return nil, 0, ErrNotFound
	default:
		resp.Body.Close()
		return nil, 0, fmt.Errorf("ociclient: GET blob: status %d", resp.StatusCode)
	}
}

// ensureConfigBlob pushes the shared empty-config blob once per session.
func (c *Client) ensureConfigBlob() error {
	if c.configPushed {
		return nil
	}
	cfgHash := objstore.MustHash(strings.TrimPrefix(emptyConfig.Digest, "sha256:"))
	if err := c.PushBlob(cfgHash, bytes.NewReader(emptyConfigBlob), int64(len(emptyConfigBlob))); err != nil {
		return fmt.Errorf("ociclient: push empty config: %w", err)
	}
	c.configPushed = true
	return nil
}

// PutManifest uploads a manifest under a tag (PUT .../manifests/<tag>). It
// first ensures the shared empty-config blob exists.
func (c *Client) PutManifest(tag string, body []byte, mediaType string) error {
	if err := c.ensureConfigBlob(); err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPut, c.manifestURL(tag), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mediaType)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("ociclient: PUT manifest %s: %w", tag, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("ociclient: PUT manifest %s: status %d: %s", tag, resp.StatusCode, b)
	}
	return nil
}

// GetManifest fetches a manifest by tag or digest. ok=false on 404.
func (c *Client) GetManifest(ref string) (body []byte, mediaType string, ok bool, err error) {
	req, err := http.NewRequest(http.MethodGet, c.manifestURL(ref), nil)
	if err != nil {
		return nil, "", false, err
	}
	req.Header.Set("Accept", MediaTypeManifest)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, "", false, err
		}
		return b, resp.Header.Get("Content-Type"), true, nil
	case http.StatusNotFound:
		return nil, "", false, nil
	default:
		return nil, "", false, fmt.Errorf("ociclient: GET manifest %s: status %d", ref, resp.StatusCode)
	}
}

// tagList is the /tags/list response body.
type tagList struct {
	Tags []string `json:"tags"`
}

// ListTags returns all tags in the repository, following Link pagination.
func (c *Client) ListTags() ([]string, error) {
	next := fmt.Sprintf("%s/v2/%s/tags/list", c.base, c.name)
	var all []string
	for next != "" {
		resp, err := c.http.Get(next)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusNotFound {
			// Empty/absent repository — no tags.
			resp.Body.Close()
			return all, nil
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("ociclient: GET tags: status %d", resp.StatusCode)
		}
		var tl tagList
		if err := json.NewDecoder(resp.Body).Decode(&tl); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()
		all = append(all, tl.Tags...)
		next = nextLink(resp.Header.Get("Link"), c.base)
	}
	return all, nil
}

// nextLink extracts the rel="next" URL from an RFC 5988 Link header, resolving
// a relative path against base. Returns "" if absent.
func nextLink(link, base string) string {
	if link == "" {
		return ""
	}
	for _, part := range strings.Split(link, ",") {
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		lo := strings.IndexByte(part, '<')
		hi := strings.IndexByte(part, '>')
		if lo < 0 || hi < 0 || hi <= lo {
			continue
		}
		u := part[lo+1 : hi]
		if strings.HasPrefix(u, "/") {
			u = base + u
		}
		return u
	}
	return ""
}
