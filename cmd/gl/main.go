package main

import (
	"fmt"
	"os"

	"gl-ng/internal/log"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "build":
		err = cmdBuild(args)
	case "graph":
		err = cmdGraph(args)
	case "cache":
		err = cmdCache(args)
	case "import":
		err = cmdImport(args)
	case "lockfile":
		err = cmdLockfile(args)
	case "lockfile-rootfs":
		err = cmdLockfileRootfs(args)
	case "status":
		err = cmdStatus(args)
	case "exec-chroot":
		err = cmdExecChroot(args)
	case "resolve":
		err = cmdResolve(args)
	case "help", "--help", "-h":
		usage()
		return
	default:
		_, l := rootContext(log.Engine)
		l.Error("unknown command %q", cmd)
		usage()
		os.Exit(2)
	}

	if err != nil {
		_, l := rootContext(log.Engine)
		l.Error("gl %s: %v", cmd, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: gl <command> [arguments]")
	fmt.Fprintln(os.Stderr, "commands: build, graph, cache, import, lockfile, lockfile-rootfs, status, exec-chroot, resolve")
}
