package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gl-ng/internal/objstore"
)

// writeSourcesYML writes the sources.yml file to the package directory.
// This file records the orig tarballs stored in the object store,
// binding them to the package by their content hashes.
//
// Format:
//
//	sources:
//	  - name: "pkg_1.0.orig.tar.xz"
//	    hash: "9209690f..."
func writeSourcesYML(pkgDir string, entries []SourceEntry) error {
	if len(entries) == 0 {
		// No sources to write (native packages have no orig tarballs)
		return nil
	}

	var sb strings.Builder
	sb.WriteString("sources:\n")
	for _, e := range entries {
		sb.WriteString(fmt.Sprintf("  - name: %q\n", e.Name))
		sb.WriteString(fmt.Sprintf("    hash: %q\n", e.Hash.String()))
	}

	sourcesPath := filepath.Join(pkgDir, "sources.yml")
	if err := os.WriteFile(sourcesPath, []byte(sb.String()), 0o644); err != nil {
		return fmt.Errorf("writing sources.yml: %w", err)
	}
	return nil
}

// ParseSourcesYML parses a sources.yml file and returns the list of source entries.
// This is useful for reading back previously imported source metadata.
func ParseSourcesYML(data []byte) ([]SourceEntry, error) {
	// Simple YAML parser for our known format.
	// We avoid importing a full YAML library for this minimal format.
	var entries []SourceEntry
	lines := strings.Split(string(data), "\n")

	var currentName string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed == "sources:" {
			continue
		}

		if strings.HasPrefix(trimmed, "- name:") {
			value := strings.TrimPrefix(trimmed, "- name:")
			currentName = unquoteYAML(strings.TrimSpace(value))
		} else if strings.HasPrefix(trimmed, "name:") {
			value := strings.TrimPrefix(trimmed, "name:")
			currentName = unquoteYAML(strings.TrimSpace(value))
		} else if strings.HasPrefix(trimmed, "hash:") {
			value := strings.TrimPrefix(trimmed, "hash:")
			hashStr := unquoteYAML(strings.TrimSpace(value))
			if currentName == "" {
				return nil, fmt.Errorf("hash without name in sources.yml")
			}
			h, err := parseHash(hashStr)
			if err != nil {
				return nil, fmt.Errorf("invalid hash for %s: %w", currentName, err)
			}
			entries = append(entries, SourceEntry{
				Name: currentName,
				Hash: h,
			})
			currentName = ""
		}
	}

	return entries, nil
}

// unquoteYAML removes surrounding quotes from a YAML value.
func unquoteYAML(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return s[1 : len(s)-1]
	}
	return s
}

// parseHash wraps objstore.NewHash for use in this package.
func parseHash(s string) (objstore.Hash, error) {
	return objstore.NewHash(s)
}
