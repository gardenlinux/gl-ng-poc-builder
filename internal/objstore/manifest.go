package objstore

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Output is a single named blob reference within an artifact's output closure:
// a leaf's name (e.g. a filename or "control:libc6") and its blob hash. It
// mirrors artifact.Output so the store can reconstruct a manifest on a
// pull-through hit without depending on the artifact package (which would be a
// cycle). See oci-cache-design.md §11.6.
type Output struct {
	Name string
	Hash Hash
}

// SerializeManifest encodes outputs in the generic manifest format,
// "<hash> <name>\n" per entry. This is the single source of truth for the
// local manifest byte format; artifact.SerializeManifest delegates here so the
// pull-through reconstruction in MapGet is byte-identical to what the engine
// writes (oci-cache-design.md §11.6).
func SerializeManifest(outputs []Output) string {
	var sb strings.Builder
	for _, out := range outputs {
		sb.WriteString(out.Hash.String())
		sb.WriteByte(' ')
		sb.WriteString(out.Name)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// serializeManifest is the internal alias used by Store.MapGet.
func serializeManifest(outputs []Output) string {
	return SerializeManifest(outputs)
}

// ParseManifest decodes the "<hash> <name>\n" manifest format read from r into
// its Output entries. It is the read counterpart to SerializeManifest, kept
// here so the format is defined in exactly one place.
func ParseManifest(r io.Reader) ([]Output, error) {
	var outputs []Output
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("malformed manifest line: %q", line)
		}
		hash, err := NewHash(parts[0])
		if err != nil {
			return nil, fmt.Errorf("invalid hash in manifest: %w", err)
		}
		outputs = append(outputs, Output{Name: parts[1], Hash: hash})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return outputs, nil
}
