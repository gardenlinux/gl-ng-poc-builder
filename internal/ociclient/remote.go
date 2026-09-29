package ociclient

import (
	"io"
	"strings"

	"gl-ng/internal/objstore"
)

// Remote adapts a Client to objstore.Remote — the pull-through backend the
// object store falls through to on a local miss (oci-cache-design.md §11.6,
// §12.2). It is constructed in cmd/gl from GL_REGISTRY and attached with
// Store.SetRemote.
type Remote struct {
	client *Client
}

// NewRemote wraps a Client as an objstore.Remote.
func NewRemote(c *Client) *Remote {
	return &Remote{client: c}
}

var _ objstore.Remote = (*Remote)(nil)

// PullBlobByDigest fetches a blob by hash. It returns ociclient.ErrNotFound
// when absent; the store treats any error as a miss.
func (r *Remote) PullBlobByDigest(h objstore.Hash) (io.ReadCloser, int64, error) {
	return r.client.PullBlob(h)
}

// PullOutputManifest fetches the output manifest for an artifact identity and
// converts its titled layers back into leaf outputs (§11.6). ok=false when the
// tag is absent remotely.
func (r *Remote) PullOutputManifest(identity objstore.Hash) ([]objstore.Output, bool, error) {
	body, _, ok, err := r.client.GetManifest(TagPrefixOutput + identity.String())
	if err != nil || !ok {
		return nil, ok, err
	}
	m, err := ParseManifest(body)
	if err != nil {
		return nil, false, err
	}
	leaves := make([]objstore.Output, 0, len(m.Layers))
	for _, layer := range m.Layers {
		h, err := objstore.NewHash(strings.TrimPrefix(layer.Digest, "sha256:"))
		if err != nil {
			return nil, false, err
		}
		leaves = append(leaves, objstore.Output{
			Name: layer.Annotations[AnnotationTitle],
			Hash: h,
		})
	}
	return leaves, true, nil
}
