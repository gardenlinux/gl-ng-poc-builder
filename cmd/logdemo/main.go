package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"gl-ng/internal/container"
	"gl-ng/internal/log"
)

func main() {
	buf := log.NewBufferTarget()
	ctx := log.WithTarget(context.Background(), buf)
	logger := log.From(ctx, log.Engine)

	base := container.NewBaseExecEnv()
	defer base.Close()

	scriptPath, err := filepath.Abs("cmd/logdemo/test_script.sh")
	if err != nil {
		fmt.Fprintf(os.Stderr, "abs path: %v\n", err)
		os.Exit(1)
	}

	stdout, stderr, closeFn := log.NewExecWriters(ctx, log.Exec)

	devNull, err := os.Open("/dev/null")
	if err != nil {
		fmt.Fprintf(os.Stderr, "open /dev/null: %v\n", err)
		os.Exit(1)
	}
	defer devNull.Close()

	logger.Debug("starting exec process: %s", scriptPath)

	pid, err := base.Exec(&container.ExecRequest{
		Argv: []string{"/bin/bash", scriptPath},
		FDs:  []*os.File{devNull, stdout, stderr},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "exec: %v\n", err)
		os.Exit(1)
	}

	// Close write ends so reader goroutines can detect EOF
	stdout.Close()
	stderr.Close()

	logger.Debug("starting log printer")
	printer := log.NewLogPrinter(buf)
	printer.Run()

	exitCode, err := base.Wait(pid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wait: %v\n", err)
		os.Exit(1)
	}

	// Drain remaining pipe output
	closeFn()

	logger.Debug("process exited with code %d", exitCode)

	logger.Debug("stopping log printer")
	printer.Stop()

	if exitCode != 0 {
		os.Exit(exitCode)
	}
}
