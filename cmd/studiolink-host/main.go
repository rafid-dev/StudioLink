// Command studiolink-host runs on the Windows machine next to Roblox Studio and
// makes Studio's MCP server reachable from VMs, containers, Tailscale peers
// and external servers.
package main

import (
	"fmt"
	"io"
	"os"

	"studiolink/internal/studio"
	"studiolink/internal/version"
)

const usage = `studiolink-host: share Roblox Studio's MCP server over the network.

Usage:
  studiolink-host <command>

Commands:
  doctor    check that this machine can run the bridge
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
		fmt.Fprintf(stdout, "studiolink-host %s (protocol %d)\n", version.Version, version.ProtocolVersion)
		return 0
	case "doctor":
		return doctor(stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

func doctor(stdout, stderr io.Writer) int {
	path, err := studio.FindLauncher()
	if err != nil {
		fmt.Fprintf(stderr, "FAIL studio launcher: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "ok   studio launcher: %s\n", path)
	return 0
}
