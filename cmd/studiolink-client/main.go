// Command studiolink-client runs next to the AI agent (in a VM, container or on a
// remote server). It gives stdio-only agents a local MCP server and, in
// dial-out mode, accepts the tunnel that studiolink-host opens.
package main

import (
	"fmt"
	"io"
	"os"

	"studiolink/internal/version"
)

const usage = `studiolink-client: connect an AI agent to a remote studiolink-host.

Usage:
  studiolink-client <command>

Commands:
  version   print the version
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "studiolink-client %s (protocol %d)\n", version.Version, version.ProtocolVersion)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}
