// Package dirhash implements deterministic directory hashing for the gl-ng
// build system. It walks a directory tree and produces a single SHA-256 hash
// that changes if and only if the tree's content changes. This is used for
// artifact identity computation — the directory hash of a package directory
// captures the source tree, build configuration, lockfile references, and
// orig tarball hashes in one shot.
//
// Safe traversal is achieved via os.OpenRoot (Go 1.24+), which confines all
// file operations to within the opened directory tree. Symlinks cannot escape
// the root boundary.
package dirhash

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Type constants for directory entries. These match the Python reference
// implementation exactly to ensure hash compatibility.
const (
	TypeRegular    byte = 0
	TypeExecutable byte = 1
	TypeDirectory  byte = 2
	TypeSymlink    byte = 3
	TypeSpecial    byte = 4
)

// HashDirectory computes a deterministic SHA-256 hash of the directory tree
// rooted at path. The hash changes if and only if the content of the tree
// changes (file contents, names, types, or executable bits).
//
// The algorithm:
//  1. List entries sorted lexicographically
//  2. For each entry, compute type byte + content hash
//  3. Feed into running SHA-256: type byte, SHA-256(entry name), 32-byte content hash
//  4. Final digest = directory hash
//
// Only regular files, directories, and symlinks are read for content. Other
// entry kinds (FIFOs, sockets, device nodes) are recorded by name and a hash
// of their mode type so that adding, removing, or swapping their kind still
// changes the directory hash, but their (unreadable) "content" is not opened.
func HashDirectory(path string) (string, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return "", fmt.Errorf("dirhash: open root %q: %w", path, err)
	}
	defer root.Close()

	return hashDir(root, path, ".")
}

// hashDir recursively hashes the directory at relPath within the given root.
// rootPath is the absolute filesystem path to the root, needed for readlink.
func hashDir(root *os.Root, rootPath string, relPath string) (string, error) {
	entries, err := readDirSorted(root, relPath)
	if err != nil {
		return "", err
	}

	h := sha256.New()

	for _, entry := range entries {
		name := entry.Name()
		entryPath := joinPath(relPath, name)

		info, err := root.Lstat(entryPath)
		if err != nil {
			return "", fmt.Errorf("dirhash: lstat %q: %w", entryPath, err)
		}

		var typeByte byte
		var contentHash []byte

		mode := info.Mode()
		switch {
		case mode.IsRegular():
			typeByte = TypeRegular
			if mode&0100 != 0 { // user-execute bit
				typeByte = TypeExecutable
			}
			contentHash, err = hashFile(root, entryPath)
			if err != nil {
				return "", err
			}

		case mode.IsDir():
			typeByte = TypeDirectory
			hexHash, err := hashDir(root, rootPath, entryPath)
			if err != nil {
				return "", err
			}
			contentHash, err = hex.DecodeString(hexHash)
			if err != nil {
				return "", fmt.Errorf("dirhash: internal error decoding hash: %w", err)
			}

		case mode&os.ModeSymlink != 0:
			typeByte = TypeSymlink
			contentHash, err = hashSymlink(rootPath, entryPath)
			if err != nil {
				return "", err
			}

		default:
			typeByte = TypeSpecial
			d := sha256.Sum256([]byte(mode.Type().String()))
			contentHash = d[:]
		}

		// Feed: type byte, SHA-256(entry name), 32-byte content hash
		h.Write([]byte{typeByte})
		nameDigest := sha256.Sum256([]byte(name))
		h.Write(nameDigest[:])
		h.Write(contentHash)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// readDirSorted reads directory entries and returns them sorted lexicographically.
func readDirSorted(root *os.Root, relPath string) ([]os.DirEntry, error) {
	// Open the directory within the root for reading entries.
	// os.Root doesn't have a ReadDir method directly, so we open the dir
	// and use the file's ReadDir method.
	var f *os.File
	var err error
	if relPath == "." {
		f, err = root.Open(".")
	} else {
		f, err = root.Open(relPath)
	}
	if err != nil {
		return nil, fmt.Errorf("dirhash: open dir %q: %w", relPath, err)
	}
	defer f.Close()

	entries, err := f.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("dirhash: read dir %q: %w", relPath, err)
	}

	slices.SortFunc(entries, func(a, b os.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})

	return entries, nil
}

// hashFile computes SHA-256 of a regular file's content.
func hashFile(root *os.Root, relPath string) ([]byte, error) {
	f, err := root.Open(relPath)
	if err != nil {
		return nil, fmt.Errorf("dirhash: open file %q: %w", relPath, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, fmt.Errorf("dirhash: read file %q: %w", relPath, err)
	}

	return h.Sum(nil), nil
}

// hashSymlink computes SHA-256 of the symlink target string.
// Uses os.Readlink on the full filesystem path since os.Root doesn't have Readlink.
func hashSymlink(rootPath, relPath string) ([]byte, error) {
	// Construct the full path to read the symlink target.
	// This is safe because we already verified via root.Lstat() that the entry
	// is a symlink, and we only hash its target string (not following it).
	fullPath := filepath.Join(rootPath, relPath)
	target, err := os.Readlink(fullPath)
	if err != nil {
		return nil, fmt.Errorf("dirhash: readlink %q: %w", relPath, err)
	}

	digest := sha256.Sum256([]byte(target))
	return digest[:], nil
}

// joinPath joins a relative base path with a child name.
func joinPath(base, name string) string {
	if base == "." {
		return name
	}
	return base + "/" + name
}
