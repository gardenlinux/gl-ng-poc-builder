package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"gl-ng/internal/build"
	"gl-ng/internal/buildcfg"
	"gl-ng/internal/log"
	"gl-ng/internal/objstore"
	"gl-ng/internal/ociclient"
)

func cmdCache(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gl cache <subcommand>")
	}

	sub := args[0]
	subArgs := args[1:]

	switch sub {
	case "status":
		return cacheStatus(subArgs)
	case "gc":
		return cacheGC(subArgs)
	case "push":
		return cachePush(subArgs)
	case "blobs":
		return cacheBlobs(subArgs)
	case "map":
		return cacheMap(subArgs)
	case "pin":
		return cachePin(subArgs)
	default:
		return fmt.Errorf("unknown cache subcommand %q", sub)
	}
}

func cacheStatus(args []string) error {
	store, err := openStore("")
	if err != nil {
		return err
	}

	var blobCount int
	store.Blobs.Iterate(func(h objstore.Hash) error {
		blobCount++
		return nil
	})

	var mapCount int
	store.Map.Iterate(func(k objstore.Hash) error {
		mapCount++
		return nil
	})

	pins := store.Pins.List()

	_, l := rootContext(log.Engine)
	l.Info("cache: %s", store.Root())
	l.Info("blobs: %d, map entries: %d, pins: %d", blobCount, mapCount, len(pins))

	ref := registryRef("")
	if ref == "" {
		l.Info("registry: none (set GL_REGISTRY to enable pull-through)")
	} else {
		regHost, repo := ociclient.SplitRef(ref)
		reach := "unreachable"
		if repo != "" && ociclient.New(regHost, repo).Reachable() {
			reach = "reachable"
		}
		l.Info("registry: %s (%s)", ref, reach)
	}
	return nil
}

// cacheGC computes the keep-set from the current checkout's build graph plus
// every local pin, then sweeps everything else. See oci-cache-design.md §5:
// a blob is kept iff it is graph-reachable from the current checkout OR named
// by a pin. Everything else is remote-mirrored (re-fetchable) or a stale,
// rebuildable output.
func cacheGC(args []string) error {
	fs := flag.NewFlagSet("gc", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "show what would be deleted")
	arch := fs.String("arch", buildcfg.HostArch(), "target architecture for the build graph")
	confDir := fs.String("conf-dir", "", "configuration directory (contains pkgs/, rootfs.yml)")
	stubPath := fs.String("stub", "", "path to exec_env_stub binary")
	fs.Parse(args)

	store, err := openStore("")
	if err != nil {
		return err
	}

	_, l := rootContext(log.Engine)

	keep := make(map[objstore.Hash]struct{})

	// (a) Graph reachability: for every artifact the current checkout can
	// produce, keep its manifest hash and all its leaf hashes. Unbuilt nodes
	// return an error from OutputRefs and are skipped.
	confRoot := *confDir
	if confRoot == "" {
		confRoot = findConfDir()
	}
	if confRoot == "" {
		l.Warn("no conf-dir found; GC will protect only pinned blobs (all build outputs collectible)")
	} else {
		graphResult, err := build.BuildGraph(build.GraphConfig{
			ConfDir:  confRoot,
			Arch:     *arch,
			Store:    store,
			StubPath: *stubPath,
		})
		if err != nil {
			return fmt.Errorf("build graph: %w", err)
		}
		reachable := 0
		for _, key := range graphResult.Graph.Keys() {
			a := graphResult.Graph.Find(key)
			manifest, leaves, err := a.OutputRefs(store)
			if err != nil {
				continue // not built — contributes nothing
			}
			keep[manifest] = struct{}{}
			for _, h := range leaves {
				keep[h] = struct{}{}
			}
			reachable++
		}
		l.Info("graph: %d nodes, %d built and reachable", graphResult.Graph.Len(), reachable)
	}

	// (b) Pins: every blob named by any local pin.
	pinned := store.Pins.ReachableBlobs()
	for h := range pinned {
		keep[h] = struct{}{}
	}
	l.Info("keep-set: %d blobs (%d from pins)", len(keep), len(pinned))

	if *dryRun {
		var wouldDelete int
		store.Blobs.Iterate(func(h objstore.Hash) error {
			if _, ok := keep[h]; !ok {
				wouldDelete++
			}
			return nil
		})
		l.Info("would delete %d unreachable blobs", wouldDelete)
		return nil
	}

	deleted, err := store.Blobs.Sweep(keep)
	if err != nil {
		return fmt.Errorf("sweep: %w", err)
	}
	l.Info("deleted %d unreachable blobs", deleted)
	return nil
}

// cachePush publishes local build outputs and/or pins to an OCI registry
// (oci-cache-design.md §12.3). Outputs are walked from the current checkout's
// build graph (same traversal as gc); each built node's manifest + leaf blobs
// are pushed and tagged build-artifact-<identity>. Pins are pushed as their
// blob closure plus a pin manifest tagged <kind>-<id>. Pushes are idempotent:
// blobs already present are skipped, and an existing tag is left alone unless
// --force. Content addressing makes every push verifiable by the puller.
func cachePush(args []string) error {
	fs := flag.NewFlagSet("push", flag.ExitOnError)
	registry := fs.String("registry", "", "registry+repo, e.g. localhost:5000/gl-ng (or GL_REGISTRY)")
	confDir := fs.String("conf-dir", "", "configuration directory (contains pkgs/, rootfs.yml)")
	arch := fs.String("arch", buildcfg.HostArch(), "target architecture for the build graph")
	stubPath := fs.String("stub", "", "path to exec_env_stub binary")
	pushOutputs := fs.Bool("outputs", false, "push build outputs (default: push both if neither flag given)")
	pushPins := fs.Bool("pins", false, "push pins (default: push both if neither flag given)")
	dryRun := fs.Bool("dry-run", false, "show what would be pushed without pushing")
	force := fs.Bool("force", false, "re-push manifests even if the tag already exists")
	fs.Parse(args)

	// Neither selector given ⇒ push both.
	if !*pushOutputs && !*pushPins {
		*pushOutputs, *pushPins = true, true
	}

	ref := *registry
	if ref == "" {
		ref = os.Getenv("GL_REGISTRY")
	}
	if ref == "" {
		return fmt.Errorf("no registry: pass --registry or set GL_REGISTRY")
	}
	regHost, repo := ociclient.SplitRef(ref)
	if repo == "" {
		return fmt.Errorf("registry ref %q must include a repository, e.g. localhost:5000/gl-ng", ref)
	}
	client := ociclient.New(regHost, repo)

	store, err := openStore("")
	if err != nil {
		return err
	}

	_, l := rootContext(log.Engine)
	if *dryRun {
		l.Info("dry-run: no blobs or manifests will be pushed")
	}

	if *pushOutputs {
		if err := pushOutputsToRegistry(client, store, *confDir, *arch, *stubPath, *dryRun, *force, l); err != nil {
			return err
		}
	}
	if *pushPins {
		if err := pushPinsToRegistry(client, store, *dryRun, *force, l); err != nil {
			return err
		}
	}
	return nil
}

// pushBlobIfMissing pushes a local blob to the registry unless it is already
// present there. Size comes from the on-disk blob. Honors dry-run.
func pushBlobIfMissing(client *ociclient.Client, store *objstore.Store, h objstore.Hash, dryRun bool) (int64, error) {
	fi, err := os.Stat(store.Blobs.Path(h))
	if err != nil {
		return 0, fmt.Errorf("stat blob %s: %w", h, err)
	}
	size := fi.Size()
	if dryRun {
		return size, nil
	}
	has, err := client.HasBlob(h)
	if err != nil {
		return size, fmt.Errorf("head blob %s: %w", h, err)
	}
	if has {
		return size, nil
	}
	rc, err := store.Blobs.Open(h)
	if err != nil {
		return size, fmt.Errorf("open blob %s: %w", h, err)
	}
	defer rc.Close()
	if err := client.PushBlob(h, rc, size); err != nil {
		return size, fmt.Errorf("push blob %s: %w", h, err)
	}
	return size, nil
}

func pushOutputsToRegistry(client *ociclient.Client, store *objstore.Store, confDir, arch, stubPath string, dryRun, force bool, l *log.Logger) error {
	confRoot := confDir
	if confRoot == "" {
		confRoot = findConfDir()
	}
	if confRoot == "" {
		l.Warn("no conf-dir found; skipping outputs push")
		return nil
	}
	graphResult, err := build.BuildGraph(build.GraphConfig{
		ConfDir:  confRoot,
		Arch:     arch,
		Store:    store,
		StubPath: stubPath,
	})
	if err != nil {
		return fmt.Errorf("build graph: %w", err)
	}

	pushed := 0
	for _, key := range graphResult.Graph.Keys() {
		a := graphResult.Graph.Find(key)
		manifest, leaves, err := a.OutputRefs(store)
		if err != nil {
			continue // not built — nothing to publish
		}
		identity, err := a.Identity()
		if err != nil {
			l.Warn("skip %s: identity: %v", key, err)
			continue
		}
		tag := ociclient.TagPrefixOutput + identity.String()

		if !force && !dryRun {
			if _, _, ok, err := client.GetManifest(tag); err != nil {
				return fmt.Errorf("head manifest %s: %w", tag, err)
			} else if ok {
				continue // already published
			}
		}

		// Read the local manifest blob to recover each leaf's name (the
		// "<hash> <name>" pairing) — OutputRefs alone returns only hashes.
		// Only the leaves need pushing; the puller reconstructs the manifest
		// blob from them (MapGet).
		names, err := leafNames(store, manifest)
		if err != nil {
			return fmt.Errorf("read manifest for %s: %w", key, err)
		}
		leafRefs, err := pushLeaves(client, store, leaves, names, dryRun)
		if err != nil {
			return fmt.Errorf("push leaves for %s: %w", key, err)
		}

		if dryRun {
			l.Info("would push %s (%d leaves) -> %s", key, len(leafRefs), tag)
			continue
		}
		body, err := ociclient.BuildOutputManifest(identity, leafRefs)
		if err != nil {
			return fmt.Errorf("build manifest %s: %w", tag, err)
		}
		if err := client.PutManifest(tag, body, ociclient.MediaTypeManifest); err != nil {
			return fmt.Errorf("put manifest %s: %w", tag, err)
		}
		l.Info("pushed %s (%d leaves) -> %s", key, len(leafRefs), tag)
		pushed++
	}
	l.Info("outputs: %d manifests pushed", pushed)
	return nil
}

// leafNames reads a local manifest blob and returns a leaf-hash → name map.
func leafNames(store *objstore.Store, manifestHash objstore.Hash) (map[objstore.Hash]string, error) {
	rc, err := store.Blobs.Open(manifestHash)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	outputs, err := objstore.ParseManifest(rc)
	if err != nil {
		return nil, err
	}
	names := make(map[objstore.Hash]string, len(outputs))
	for _, o := range outputs {
		names[o.Hash] = o.Name
	}
	return names, nil
}

// pushLeaves pushes each leaf blob and assembles the LeafRef list. names maps a
func pushLeaves(client *ociclient.Client, store *objstore.Store, leaves []objstore.Hash, names map[objstore.Hash]string, dryRun bool) ([]ociclient.LeafRef, error) {
	refs := make([]ociclient.LeafRef, 0, len(leaves))
	for _, h := range leaves {
		size, err := pushBlobIfMissing(client, store, h, dryRun)
		if err != nil {
			return nil, err
		}
		name := names[h]
		if name == "" {
			name = h.String()
		}
		refs = append(refs, ociclient.LeafRef{Hash: h, Name: name, Size: size})
	}
	return refs, nil
}

func pushPinsToRegistry(client *ociclient.Client, store *objstore.Store, dryRun, force bool, l *log.Logger) error {
	pins := store.Pins.List()
	pushed := 0
	for _, p := range pins {
		prefix := ociclient.TagPrefixImport
		if p.Kind == objstore.PinKindBuildDeps {
			prefix = ociclient.TagPrefixBuildDeps
		}
		tag := prefix + p.ID

		if !force && !dryRun {
			if _, _, ok, err := client.GetManifest(tag); err != nil {
				return fmt.Errorf("head manifest %s: %w", tag, err)
			} else if ok {
				continue
			}
		}

		sizes := make(map[objstore.Hash]int64, len(p.Blobs))
		for _, h := range p.Blobs {
			size, err := pushBlobIfMissing(client, store, h, dryRun)
			if err != nil {
				return fmt.Errorf("pin %s: %w", p.ID, err)
			}
			sizes[h] = size
		}

		if dryRun {
			l.Info("would push pin %s (%s, %d blobs) -> %s", p.ID, p.Name, len(p.Blobs), tag)
			continue
		}
		body, err := ociclient.BuildPinManifest(p.Kind, p.ID, p.Name, p.Blobs, sizes)
		if err != nil {
			return fmt.Errorf("build pin manifest %s: %w", tag, err)
		}
		if err := client.PutManifest(tag, body, ociclient.MediaTypeManifest); err != nil {
			return fmt.Errorf("put pin manifest %s: %w", tag, err)
		}
		l.Info("pushed pin %s (%s, %d blobs) -> %s", p.ID, p.Name, len(p.Blobs), tag)
		pushed++
	}
	l.Info("pins: %d manifests pushed", pushed)
	return nil
}

func cacheBlobs(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gl cache blobs <get|store|list|delete|check|path>")
	}

	store, err := openStore("")
	if err != nil {
		return err
	}

	switch args[0] {
	case "list":
		store.Blobs.Iterate(func(h objstore.Hash) error {
			fmt.Println(h)
			return nil
		})
		return nil
	case "check":
		if len(args) < 2 {
			return fmt.Errorf("usage: gl cache blobs check <hash>")
		}
		h, err := objstore.NewHash(args[1])
		if err != nil {
			return err
		}
		if store.Blobs.Has(h) {
			fmt.Println("exists")
		} else {
			fmt.Println("not found")
		}
		return nil
	case "path":
		if len(args) < 2 {
			return fmt.Errorf("usage: gl cache blobs path <hash>")
		}
		h, err := objstore.NewHash(args[1])
		if err != nil {
			return err
		}
		fmt.Println(store.Blobs.Path(h))
		return nil
	case "store":
		if len(args) < 2 {
			return fmt.Errorf("usage: gl cache blobs store <file>")
		}
		f, err := os.Open(args[1])
		if err != nil {
			return err
		}
		defer f.Close()
		hash, err := store.Blobs.Store(f)
		if err != nil {
			return err
		}
		fmt.Println(hash)
		return nil
	case "get":
		if len(args) < 2 {
			return fmt.Errorf("usage: gl cache blobs get <hash>")
		}
		h, err := objstore.NewHash(args[1])
		if err != nil {
			return err
		}
		r, err := store.Blobs.Open(h)
		if err != nil {
			return err
		}
		defer r.Close()
		if _, err := io.Copy(os.Stdout, r); err != nil {
			return fmt.Errorf("write blob to stdout: %w", err)
		}
		return nil
	case "delete":
		if len(args) < 2 {
			return fmt.Errorf("usage: gl cache blobs delete <hash>")
		}
		h, err := objstore.NewHash(args[1])
		if err != nil {
			return err
		}
		return store.Blobs.Delete(h)
	default:
		return fmt.Errorf("unknown blobs subcommand %q", args[0])
	}
}

func cacheMap(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gl cache map <get|set|list|delete|check>")
	}

	store, err := openStore("")
	if err != nil {
		return err
	}

	switch args[0] {
	case "list":
		store.Map.Iterate(func(k objstore.Hash) error {
			v, _ := store.Map.Get(k)
			fmt.Printf("%s -> %s\n", k, v)
			return nil
		})
		return nil
	case "get":
		if len(args) < 2 {
			return fmt.Errorf("usage: gl cache map get <key>")
		}
		k, err := objstore.NewHash(args[1])
		if err != nil {
			return err
		}
		v, err := store.Map.Get(k)
		if err != nil {
			return err
		}
		fmt.Println(v)
		return nil
	case "set":
		if len(args) < 3 {
			return fmt.Errorf("usage: gl cache map set <key> <value>")
		}
		k, err := objstore.NewHash(args[1])
		if err != nil {
			return err
		}
		v, err := objstore.NewHash(args[2])
		if err != nil {
			return err
		}
		return store.Map.Set(k, v, false)
	case "check":
		if len(args) < 2 {
			return fmt.Errorf("usage: gl cache map check <key>")
		}
		k, err := objstore.NewHash(args[1])
		if err != nil {
			return err
		}
		if store.Map.Has(k) {
			fmt.Println("exists")
		} else {
			fmt.Println("not found")
		}
		return nil
	case "delete":
		if len(args) < 2 {
			return fmt.Errorf("usage: gl cache map delete <key>")
		}
		k, err := objstore.NewHash(args[1])
		if err != nil {
			return err
		}
		return store.Map.Delete(k)
	default:
		return fmt.Errorf("unknown map subcommand %q", args[0])
	}
}

// cachePin implements the pin CLI: list / show / drop. Pins protect local-only
// input blobs from GC (oci-cache-design.md §6).
func cachePin(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gl cache pin <list|show|drop>")
	}

	store, err := openStore("")
	if err != nil {
		return err
	}

	switch args[0] {
	case "list":
		pins := store.Pins.List()
		for _, p := range pins {
			fmt.Printf("%s  %d blobs  %s\n", p.ID, len(p.Blobs), p.Name)
		}
		if len(pins) == 0 {
			fmt.Println("(no pins)")
		}
		return nil
	case "show":
		if len(args) < 2 {
			return fmt.Errorf("usage: gl cache pin show <id>")
		}
		p, err := store.Pins.Get(args[1])
		if err != nil {
			return err
		}
		fmt.Printf("id:   %s\nname: %s\nblobs (%d):\n", p.ID, p.Name, len(p.Blobs))
		for _, h := range p.Blobs {
			fmt.Printf("  %s\n", h)
		}
		return nil
	case "drop":
		if len(args) < 2 {
			return fmt.Errorf("usage: gl cache pin drop <id>")
		}
		p, err := store.Pins.Get(args[1])
		if err != nil {
			return err
		}
		if err := store.Pins.Drop(args[1]); err != nil {
			return err
		}
		_, l := rootContext(log.Engine)
		l.Info("dropped pin %s (%s)", p.ID, p.Name)
		l.Warn("if these blobs were never published to a remote, they are unrecoverable after the next GC")
		return nil
	default:
		return fmt.Errorf("unknown pin subcommand %q", args[0])
	}
}
