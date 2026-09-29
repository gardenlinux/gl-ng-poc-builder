// Package ociclient is a minimal OCI distribution v2 registry client over
// stdlib net/http, plus the ORAS-style manifest layout gl-ng publishes. It
// speaks only the verbs the pull-through cache needs (blob head/push/pull,
// manifest put/get, tag list) and defines the exact on-registry bytes fixed in
// doc/src/concepts/oci-cache-design.md §11.
package ociclient

import (
	"encoding/json"

	"gl-ng/internal/objstore"
)

// Media types and artifact types (oci-cache-design.md §11.4/§11.5).
const (
	MediaTypeManifest  = "application/vnd.oci.image.manifest.v1+json"
	MediaTypeEmptyJSON = "application/vnd.oci.empty.v1+json"
	MediaTypeOutput    = "application/vnd.gl-ng.output.v1"
	MediaTypePinBlob   = "application/vnd.gl-ng.pin-blob.v1"
	ArtifactTypeOutput = "application/vnd.gl-ng.artifact.v1"
	ArtifactTypePin    = "application/vnd.gl-ng.pin.v1"

	// Annotation keys.
	AnnotationTitle    = "org.opencontainers.image.title"
	AnnotationIdentity = "vnd.gl-ng.identity"
	AnnotationPinID    = "vnd.gl-ng.pin.id"
	AnnotationPinName  = "vnd.gl-ng.pin.name"
	AnnotationPinKind  = "vnd.gl-ng.pin.kind"
)

// Tag prefixes (oci-cache-design.md §11.3). A slash is not legal in an OCI tag,
// so the "namespace" is encoded as a tag prefix.
const (
	TagPrefixOutput    = "build-artifact-"
	TagPrefixImport    = "import-"
	TagPrefixBuildDeps = "builddeps-"
)

// emptyConfig is the OCI-standard empty config descriptor: the two bytes "{}"
// (digest sha256:44136fa3…, size 2, inline data "e30="). Every manifest shares
// it; the two bytes must still exist as a blob on the registry.
var emptyConfig = Descriptor{
	MediaType: MediaTypeEmptyJSON,
	Digest:    "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
	Size:      2,
	Data:      "e30=",
}

// emptyConfigBlob is the raw content of the empty config blob ("{}").
var emptyConfigBlob = []byte("{}")

// Descriptor is an OCI content descriptor.
type Descriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Data        string            `json:"data,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// Manifest is an OCI image manifest, ORAS-style (empty config, artifact type,
// one layer per blob).
type Manifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	ArtifactType  string            `json:"artifactType,omitempty"`
	Config        Descriptor        `json:"config"`
	Layers        []Descriptor      `json:"layers"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

// LeafRef is one output blob and its name, used to build an output manifest.
type LeafRef struct {
	Hash objstore.Hash
	Name string
	Size int64
}

// digest renders an objstore.Hash as an OCI digest string.
func digest(h objstore.Hash) string {
	return "sha256:" + h.String()
}

// BuildOutputManifest constructs the §11.4 output-artifact manifest: one titled
// layer per leaf, empty config, identity in a top-level annotation.
func BuildOutputManifest(identity objstore.Hash, leaves []LeafRef) ([]byte, error) {
	layers := make([]Descriptor, 0, len(leaves))
	for _, l := range leaves {
		layers = append(layers, Descriptor{
			MediaType:   MediaTypeOutput,
			Digest:      digest(l.Hash),
			Size:        l.Size,
			Annotations: map[string]string{AnnotationTitle: l.Name},
		})
	}
	m := Manifest{
		SchemaVersion: 2,
		MediaType:     MediaTypeManifest,
		ArtifactType:  ArtifactTypeOutput,
		Config:        emptyConfig,
		Layers:        layers,
		Annotations:   map[string]string{AnnotationIdentity: identity.String()},
	}
	return json.Marshal(m)
}

// BuildPinManifest constructs the §11.5 input-pin manifest: bare digest+size
// layers (no per-layer title), pin id/name/kind in top-level annotations.
func BuildPinManifest(kind, id, name string, blobs []objstore.Hash, sizes map[objstore.Hash]int64) ([]byte, error) {
	layers := make([]Descriptor, 0, len(blobs))
	for _, h := range blobs {
		layers = append(layers, Descriptor{
			MediaType: MediaTypePinBlob,
			Digest:    digest(h),
			Size:      sizes[h],
		})
	}
	m := Manifest{
		SchemaVersion: 2,
		MediaType:     MediaTypeManifest,
		ArtifactType:  ArtifactTypePin,
		Config:        emptyConfig,
		Layers:        layers,
		Annotations: map[string]string{
			AnnotationPinID:   id,
			AnnotationPinName: name,
			AnnotationPinKind: kind,
		},
	}
	return json.Marshal(m)
}

// ParseManifest unmarshals a manifest body.
func ParseManifest(body []byte) (Manifest, error) {
	var m Manifest
	err := json.Unmarshal(body, &m)
	return m, err
}
