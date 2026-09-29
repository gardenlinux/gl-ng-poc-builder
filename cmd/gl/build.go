package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"runtime"

	"gl-ng/internal/artifact"
	"gl-ng/internal/build"
	"gl-ng/internal/buildcfg"
	"gl-ng/internal/log"
	"gl-ng/internal/objstore"
	"gl-ng/internal/taskui"
	"golang.org/x/term"
)

func cmdBuild(args []string) error {
	if len(args) >= 2 && args[0] == "--view-logs" {
		return cmdBuildViewLogs(args[1])
	}

	defaultJobs := int(math.Sqrt(float64(runtime.NumCPU())))

	fs := flag.NewFlagSet("build", flag.ExitOnError)
	arch := fs.String("arch", buildcfg.HostArch(), "target architecture")
	jobs := fs.Int("jobs", defaultJobs, "parallel jobs")
	cacheDir := fs.String("cache", "", "cache directory")
	confDir := fs.String("conf-dir", "", "configuration directory (contains pkgs/, rootfs.yml)")
	stubPath := fs.String("stub", "", "path to exec_env_stub binary")
	registry := fs.String("registry", "", "pull-through registry+repo, e.g. localhost:5000/gl-ng (or GL_REGISTRY)")
	invalidate := fs.String("invalidate", "", "delete the map entry for the target with this Key (e.g. rootfs:gl-rootfs:amd64) and exit")
	logsOutput := fs.String("logs-output", "", "path to write the build-logs JSON snapshot (default: $TMPDIR/gl-build-*.json)")
	fs.Parse(args)

	if *jobs <= 0 {
		*jobs = defaultJobs
	}

	storeDir := *cacheDir
	if storeDir == "" {
		storeDir = objstore.DefaultRoot()
	}
	store, err := objstore.Open(storeDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	attachRemote(store, *registry)

	confRoot := *confDir
	if confRoot == "" {
		confRoot = findConfDir()
	}
	if confRoot == "" {
		return fmt.Errorf("cannot find configuration directory; use --conf-dir")
	}

	ctx, l := rootContext(log.Engine)

	graphResult, err := build.BuildGraph(build.GraphConfig{
		ConfDir:  confRoot,
		Arch:     *arch,
		Store:    store,
		StubPath: *stubPath,
	})
	if err != nil {
		return fmt.Errorf("build graph: %w", err)
	}

	if *invalidate != "" {
		a := graphResult.Graph.Find(*invalidate)
		if a == nil {
			return fmt.Errorf("no artifact with Key %q in graph", *invalidate)
		}
		identity, err := a.Identity()
		if err != nil {
			return fmt.Errorf("compute identity for %s: %w", *invalidate, err)
		}
		if !store.Map.Has(identity) {
			l.Info("no map entry for %s (identity %s) — nothing to invalidate", *invalidate, identity)
			return nil
		}
		if err := store.Map.Delete(identity); err != nil {
			return fmt.Errorf("delete map entry %s: %w", identity, err)
		}
		l.Info("invalidated %s (identity %s)", *invalidate, identity)
		return nil
	}

	l.Info("graph: %d nodes", graphResult.Graph.Len())

	engine := artifact.NewEngine(graphResult.Graph, store, *jobs)
	results, err := engine.RunWithUI(ctx, *logsOutput)
	if err != nil {
		return fmt.Errorf("run engine: %w", err)
	}

	var failed int
	for _, r := range results {
		if r.Err != nil {
			failed++
		}
	}

	rootfsID, err := graphResult.Rootfs.Identity()
	if err == nil {
		l.Info("rootfs identity: %s", rootfsID)
	}

	if failed > 0 {
		return fmt.Errorf("%d artifact(s) failed", failed)
	}
	return nil
}

func cmdBuildViewLogs(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	tracker, err := taskui.Deserialize(f)
	if err != nil {
		return fmt.Errorf("deserialize %s: %w", path, err)
	}

	if !term.IsTerminal(int(os.Stderr.Fd())) {
		tracker.PrintPlain()
		return nil
	}

	overview := taskui.NewTaskOverview(tracker)
	overview.OnEnter = func(task *taskui.Task, stop <-chan struct{}) {
		fmt.Fprintf(os.Stderr, "--- logs: %s (press q to return) ---\n", task.Name)
		printer := log.NewLogPrinter(task.Log)
		printer.Run()

		done := make(chan struct{})
		go func() {
			buf := make([]byte, 1)
			for {
				n, err := os.Stdin.Read(buf)
				if err != nil {
					break
				}
				if n == 1 && buf[0] == 'q' {
					break
				}
			}
			close(done)
		}()

		select {
		case <-done:
		case <-stop:
		}
		printer.Stop()
		fmt.Fprintf(os.Stderr, "--- end logs ---\n")
	}
	overview.Show()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig

	overview.Hide()
	return nil
}
