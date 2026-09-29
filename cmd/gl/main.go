// Command gl is the command-line entry point to the gl-ng build system.
//
// Subcommands are dispatched from main and grow as the system does; each lives
// in its own file. Today the system can import Debian source packages into the
// object store.
package main

import (
	"fmt"
	"os"

	"gl-ng/internal/log"
)

// version is overridable at link time (-X main.version=...) once releases exist.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "import":
		err = cmdImport(args)
	case "version", "--version":
		fmt.Printf("gl %s\n", version)
		return
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
	fmt.Fprintln(os.Stderr, "commands: import, version, help")
}
