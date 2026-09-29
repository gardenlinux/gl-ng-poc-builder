package main

import (
	"context"
	"os"
	"path/filepath"

	"gl-ng/internal/log"
	"gl-ng/internal/objstore"
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
