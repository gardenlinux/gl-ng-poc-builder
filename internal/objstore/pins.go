package objstore

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"

	"gopkg.in/yaml.v3"
)

// pinIDPattern matches a valid pin id: exactly 16 lowercase hex characters
// (64 random bits). It is also used to filter pins/*.yml on read so that
// stray files never masquerade as pins.
var pinIDPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

// Pin is a named protection record for a set of local-only blobs. A pin exists
// solely to keep blobs that are imported/lockfiled locally but not (yet)
// available from a remote from being garbage-collected. See
// doc/src/concepts/oci-cache-design.md §6.
type Pin struct {
	// ID is the 16-hex-char pin identity (also the pins/<id>.yml basename).
	// It is not serialized into the file body — the filename carries it.
	ID string `yaml:"-"`
	// Name is free-form human text describing the pin's origin (package,
	// version, suite, source repo, InRelease timestamp).
	Name string `yaml:"name"`
	// Blobs is the flat list of blob hashes this pin protects.
	Blobs []Hash `yaml:"blobs"`
}

// pinFile is the on-disk YAML shape. Blobs are stored as hex strings.
type pinFile struct {
	Name  string   `yaml:"name"`
	Blobs []string `yaml:"blobs"`
}

// Pins manages the object store's pin records, one file per pin under pins/.
type Pins struct {
	root string // path to the pins/ directory
	mu   sync.Mutex
}

// newPins creates a Pins instance rooted at the given directory, creating it
// if necessary.
func newPins(root string) (*Pins, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("creating pins directory: %w", err)
	}
	return &Pins{root: root}, nil
}

// newPinID generates a fresh 16-hex-char (64-bit) pin id from crypto/rand.
func newPinID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating pin id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func (p *Pins) path(id string) string {
	return filepath.Join(p.root, id+".yml")
}

// Create generates a fresh id, writes the pin file atomically (temp + rename),
// and returns the id. Pin creation is load-bearing: callers that store
// local-only blobs must create the pin before reporting success, so a GC
// running in between cannot delete unrecoverable local work.
func (p *Pins) Create(name string, blobs []Hash) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	id, err := newPinID()
	if err != nil {
		return "", err
	}
	// Vanishingly unlikely, but never clobber an existing pin file.
	for {
		if _, statErr := os.Stat(p.path(id)); os.IsNotExist(statErr) {
			break
		}
		if id, err = newPinID(); err != nil {
			return "", err
		}
	}

	if err := p.write(id, Pin{Name: name, Blobs: blobs}); err != nil {
		return "", err
	}
	return id, nil
}

// write serializes and atomically writes a pin file.
func (p *Pins) write(id string, pin Pin) error {
	hexes := make([]string, 0, len(pin.Blobs))
	for _, h := range pin.Blobs {
		hexes = append(hexes, h.String())
	}
	data, err := yaml.Marshal(pinFile{Name: pin.Name, Blobs: hexes})
	if err != nil {
		return fmt.Errorf("marshal pin %s: %w", id, err)
	}

	tmp, err := os.CreateTemp(p.root, ".pin-tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp pin file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("writing pin file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("closing temp pin file: %w", err)
	}
	if err := os.Rename(tmpPath, p.path(id)); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("renaming pin file: %w", err)
	}
	return nil
}

// load reads and parses a single pin file by id. Returns an error the caller
// can decide to tolerate (List skips; Get propagates).
func (p *Pins) load(id string) (Pin, error) {
	data, err := os.ReadFile(p.path(id))
	if err != nil {
		return Pin{}, err
	}
	var pf pinFile
	if err := yaml.Unmarshal(data, &pf); err != nil {
		return Pin{}, fmt.Errorf("parse pin %s: %w", id, err)
	}
	blobs := make([]Hash, 0, len(pf.Blobs))
	for _, hexStr := range pf.Blobs {
		h, err := NewHash(hexStr)
		if err != nil {
			return Pin{}, fmt.Errorf("pin %s: invalid blob hash %q: %w", id, hexStr, err)
		}
		blobs = append(blobs, h)
	}
	return Pin{ID: id, Name: pf.Name, Blobs: blobs}, nil
}

// ids returns the sorted list of valid pin ids present on disk.
func (p *Pins) ids() ([]string, error) {
	entries, err := os.ReadDir(p.root)
	if err != nil {
		return nil, fmt.Errorf("reading pins directory: %w", err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) != ".yml" {
			continue
		}
		id := name[:len(name)-len(".yml")]
		if !pinIDPattern.MatchString(id) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

// List returns every valid pin. Malformed pin files are skipped (not fatal),
// mirroring the store's tolerance for stray files.
func (p *Pins) List() []Pin {
	p.mu.Lock()
	defer p.mu.Unlock()

	ids, err := p.ids()
	if err != nil {
		return nil
	}
	var pins []Pin
	for _, id := range ids {
		pin, err := p.load(id)
		if err != nil {
			// Skip malformed pin; don't fail the whole listing.
			continue
		}
		pins = append(pins, pin)
	}
	return pins
}

// Get returns a single pin by id.
func (p *Pins) Get(id string) (Pin, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !pinIDPattern.MatchString(id) {
		return Pin{}, fmt.Errorf("invalid pin id %q", id)
	}
	pin, err := p.load(id)
	if err != nil {
		if os.IsNotExist(err) {
			return Pin{}, fmt.Errorf("pin %s not found", id)
		}
		return Pin{}, err
	}
	return pin, nil
}

// Drop deletes a pin file. Dropping a pin for local-only, unpublished blobs is
// irreversible after GC (§6) — the pin is the durability guarantee.
func (p *Pins) Drop(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !pinIDPattern.MatchString(id) {
		return fmt.Errorf("invalid pin id %q", id)
	}
	if err := os.Remove(p.path(id)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("pin %s not found", id)
		}
		return fmt.Errorf("dropping pin %s: %w", id, err)
	}
	return nil
}

// ReachableBlobs returns the union of every pin's blobs, for use as GC roots.
// A blob listed by multiple pins appears once.
func (p *Pins) ReachableBlobs() map[Hash]struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()

	keep := make(map[Hash]struct{})
	ids, err := p.ids()
	if err != nil {
		return keep
	}
	for _, id := range ids {
		pin, err := p.load(id)
		if err != nil {
			continue
		}
		for _, h := range pin.Blobs {
			keep[h] = struct{}{}
		}
	}
	return keep
}
