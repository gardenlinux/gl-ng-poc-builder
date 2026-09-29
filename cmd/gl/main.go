// Command gl is the command-line entry point to the gl-ng build system.
//
// This is the initial skeleton: it establishes the subcommand-dispatch shape
// that the build, import, lockfile, and cache commands will hang off of as the
// system grows. For now it only knows how to report its version and usage.
package main

import (
	"fmt"
	"os"
)

// version is overridable at link time (-X main.version=...) once releases exist.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "version", "--version":
		fmt.Printf("gl %s\n", version)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "gl: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: gl <command> [arguments]")
	fmt.Fprintln(os.Stderr, "commands: version, help")
}
