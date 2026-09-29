package main

import (
	"flag"
	"fmt"

	"gl-ng/internal/buildcfg"
	"gl-ng/internal/lockfile"
	"gl-ng/internal/log"
	"gl-ng/internal/objstore"
)

func cmdLockfile(args []string) error {
	fs := flag.NewFlagSet("lockfile", flag.ExitOnError)
	repo := fs.String("repo", "https://deb.debian.org/debian", "APT repository URL")
	dist := fs.String("dist", "testing", "distribution")
	arch := fs.String("arch", buildcfg.HostArch(), "target architecture")
	cacheDir := fs.String("cache", "", "cache directory")
	outputDir := fs.String("output", ".", "output directory (monorepo root)")
	cookie := fs.String("cookie", "", "InRelease cache cookie (reuse cached InRelease within a session)")
	fs.Parse(args)

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: gl lockfile [flags] <package-name>")
	}

	pkgName := fs.Arg(0)

	storeDir := *cacheDir
	if storeDir == "" {
		storeDir = objstore.DefaultRoot()
	}
	store, err := objstore.Open(storeDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}

	ctx, l := rootContext(log.Lockfile)

	cfg := lockfile.Config{
		Ctx:       ctx,
		Store:     store,
		RepoURL:   *repo,
		Dist:      *dist,
		Arch:      *arch,
		OutputDir: *outputDir,
		PkgName:   pkgName,
		Cookie:    *cookie,
	}

	result, err := lockfile.Generate(cfg)
	if err != nil {
		return err
	}

	l.Info("generated lockfile for %s/%s: blob=%s packages=%d", pkgName, result.Arch, result.BlobHash, result.Packages)
	return nil
}

func cmdLockfileRootfs(args []string) error {
	fs := flag.NewFlagSet("lockfile-rootfs", flag.ExitOnError)
	repo := fs.String("repo", "https://deb.debian.org/debian", "APT repository URL")
	dist := fs.String("dist", "testing", "distribution")
	arch := fs.String("arch", buildcfg.HostArch(), "target architecture")
	cacheDir := fs.String("cache", "", "cache directory")
	outputDir := fs.String("output", ".", "output directory (staging root)")
	cookie := fs.String("cookie", "", "InRelease cache cookie (reuse cached InRelease within a session)")
	fs.Parse(args)

	storeDir := *cacheDir
	if storeDir == "" {
		storeDir = objstore.DefaultRoot()
	}
	store, err := objstore.Open(storeDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}

	ctx, l := rootContext(log.Lockfile)

	result, err := lockfile.GenerateRootfs(lockfile.RootfsConfig{
		Ctx:       ctx,
		Store:     store,
		RepoURL:   *repo,
		Dist:      *dist,
		Arch:      *arch,
		OutputDir: *outputDir,
		Cookie:    *cookie,
	})
	if err != nil {
		return err
	}

	l.Info("generated rootfs lockfile for %s: blob=%s packages=%d", result.Arch, result.BlobHash, result.Packages)
	return nil
}

func cmdStatus(args []string) error {
	_, l := rootContext(log.Engine)
	l.Info("gl-ng build system - Phase 1")
	l.Info("components: objstore, stream, deb822, depends, version, index, resolver, container, artifact, build, importer, lockfile")
	return nil
}
