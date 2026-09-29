package main

import (
	"context"
	"os"
	"path/filepath"

	"gl-ng/internal/log"
	"gl-ng/internal/objstore"
	"gl-ng/internal/ociclient"
)

func findConfDir() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "pkgs")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "rootfs.yml")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func openStore(dir string) (*objstore.Store, error) {
	if dir == "" {
		dir = objstore.DefaultRoot()
	}
	return objstore.Open(dir)
}

// registryRef resolves the registry+repo to use for pull-through: the explicit
// flag value wins, else GL_REGISTRY, else empty (pure-local, no remote).
func registryRef(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	return os.Getenv("GL_REGISTRY")
}

// attachRemote wires a pull-through remote onto store when a registry is
// configured (via --registry flag or GL_REGISTRY). A missing or malformed ref
// leaves the store pure-local. Use only on read-path commands (build, rootfs,
// lockfile install) — never on cache-admin, which must show local truth.
func attachRemote(store *objstore.Store, flagVal string) {
	ref := registryRef(flagVal)
	if ref == "" {
		return
	}
	regHost, repo := ociclient.SplitRef(ref)
	if repo == "" {
		return
	}
	store.SetRemote(ociclient.NewRemote(ociclient.New(regHost, repo)))
}

// rootContext returns a context with a console log target attached and a
// logger for the given component.
func rootContext(c log.Component) (context.Context, *log.Logger) {
	ctx := log.WithTarget(context.Background(), log.NewConsoleTarget())
	return ctx, log.From(ctx, c)
}

// rootContextStderr is like rootContext but installs a console target that
// routes every level to stderr. Use it for commands whose stdout is reserved
// for machine-parseable output.
func rootContextStderr(c log.Component) (context.Context, *log.Logger) {
	ctx := log.WithTarget(context.Background(), log.NewStderrConsoleTarget())
	return ctx, log.From(ctx, c)
}
