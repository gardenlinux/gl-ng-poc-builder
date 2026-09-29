package main

import (
	"flag"
	"fmt"

	"gl-ng/internal/importer"
	"gl-ng/internal/log"
	"gl-ng/internal/objstore"
)

func cmdImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	repo := fs.String("repo", "https://deb.debian.org/debian", "APT repository URL")
	dist := fs.String("dist", "testing", "distribution")
	keyring := fs.String("keyring", "/usr/share/keyrings/debian-archive-keyring.gpg", "GPG keyring path")
	cacheDir := fs.String("cache", "", "cache directory")
	outputDir := fs.String("output", ".", "output directory for package files")
	noVerify := fs.Bool("no-verify", false, "skip GPG signature verification")
	cookie := fs.String("cookie", "", "InRelease cache cookie (reuse cached InRelease within a session)")
	fs.Parse(args)

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: gl import [flags] <package-name>")
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

	ctx, l := rootContext(log.Importer)

	cfg := importer.ImportConfig{
		Ctx:       ctx,
		Store:     store,
		RepoURL:   *repo,
		Dist:      *dist,
		Keyring:   *keyring,
		OutputDir: *outputDir,
		NoVerify:  *noVerify,
		Cookie:    *cookie,
	}

	result, err := importer.Import(cfg, pkgName)
	if err != nil {
		return err
	}

	l.Info("imported %s %s (format: %s)", result.Name, result.Version, result.Format)
	for _, s := range result.Sources {
		l.Info("orig: %s (%s)", s.Name, s.Hash)
	}
	return nil
}
