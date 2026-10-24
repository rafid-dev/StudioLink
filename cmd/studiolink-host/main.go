// Command studiolink-host runs on the Windows machine next to Roblox Studio
// and makes Studio's MCP server reachable from VMs, containers, Tailscale
// peers and external servers.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"text/tabwriter"

	"studiolink/internal/config"
	"studiolink/internal/netif"
	"studiolink/internal/studio"
	"studiolink/internal/version"
)

const usage = `studiolink-host: share Roblox Studio's MCP server over the network.

Usage:
  studiolink-host <command> [flags]

Commands:
  setup       install StudioLink, create the config and start it (what double-clicking does)
  connect     print the commands that connect an agent: connect [docker|wsl|vm|tailscale|server]
  start       start the bridge in the background unless it is running (Windows)
  stop        stop the running bridge
  add-relay   dial out to a relay: add-relay wss://server:44771 sha256:<fingerprint>
  init        create the config file with a new token
  serve       run the bridge in this console
  tray        run the bridge in the background with a notification-area icon (Windows)
  autostart   show or set whether the tray starts at login: autostart [on|off] (Windows)
  settings    open the settings page in your browser
  status      show a running bridge's listeners, relays and sessions
  token       print the token clients must send (-new: make a client token)
  doctor      check that this machine can run the bridge
  interfaces  list network adapters the bridge can serve on
  version     print the version

Run "studiolink-host <command> -h" for a command's flags.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		if openedFromExplorer() {
			return runDoubleClick(stdout, stderr)
		}
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version":
		fmt.Fprintf(stdout, "studiolink-host %s (protocol %d)\n", version.Version, version.ProtocolVersion)
		return 0
	case "setup":
		return runSetup(rest, os.Stdin, stdout, stderr)
	case "connect":
		return runConnect(rest, stdout, stderr)
	case "settings":
		return runSettings(rest, stdout, stderr)
	case "start":
		return runStart(rest, stdout, stderr)
	case "stop":
		return runStop(rest, stdout, stderr)
	case "add-relay":
		return runAddRelay(rest, stdout, stderr)
	case "init":
		return runInit(rest, stdout, stderr)
	case "serve":
		return runServe(rest, stderr)
	case "tray":
		return runTray(rest, stdout, stderr)
	case "autostart":
		return runAutostart(rest, stdout, stderr)
	case "token":
		return runToken(rest, stdout, stderr)
	case "status":
		return runStatus(rest, stdout, stderr)
	case "interfaces":
		return runInterfaces(rest, stdout, stderr)
	case "doctor":
		return runDoctor(rest, stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usage)
		return 2
	}
}

// executable is this program's path.
func executable() string {
	exe, err := os.Executable()
	if err != nil {
		return "studiolink-host.exe"
	}
	return exe
}

// newFlags returns a flag set with the shared -config flag.
func newFlags(name string, stderr io.Writer) (*flag.FlagSet, *string) {
	fset := flag.NewFlagSet("studiolink-host "+name, flag.ContinueOnError)
	fset.SetOutput(stderr)
	def, err := config.DefaultPath()
	if err != nil {
		def = config.FileName
	}
	return fset, fset.String("config", def, "config file path")
}

func runInit(args []string, stdout, stderr io.Writer) int {
	fset, path := newFlags("init", stderr)
	force := fset.Bool("force", false, "overwrite an existing config (makes a new token)")
	if fset.Parse(args) != nil {
		return 2
	}
	if _, err := os.Stat(*path); err == nil && !*force {
		fmt.Fprintf(stderr, "%s already exists; use -force to replace it and its token\n", *path)
		return 1
	}
	cfg, err := config.Default()
	if err == nil {
		err = cfg.Save(*path)
	}
	if err != nil {
		fmt.Fprintf(stderr, "init: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Wrote %s (listening on %s).\n\n", *path, strings.Join(cfg.Listen, ", "))
	fmt.Fprintf(stdout, "Start the bridge:   studiolink-host serve\n")
	fmt.Fprintf(stdout, "Show the token:     studiolink-host token\n\n")
	fmt.Fprintf(stdout, "Claude Code in Docker on this PC:\n")
	fmt.Fprintf(stdout, "  claude mcp add --transport http Roblox_Studio http://host.docker.internal:%d/mcp --header \"Authorization: Bearer <token>\"\n", config.DefaultPort)
	return 0
}

func runToken(args []string, stdout, stderr io.Writer) int {
	fset, path := newFlags("token", stderr)
	fresh := fset.Bool("new", false, `print a new random token for a "clients" entry instead`)
	if fset.Parse(args) != nil {
		return 2
	}
	if *fresh {
		token, err := config.NewToken()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, token)
		return 0
	}
	cfg, err := loadConfig(*path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, cfg.Token)
	return 0
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	fset, path := newFlags("doctor", stderr)
	if fset.Parse(args) != nil {
		return 2
	}
	failed := false
	if launcher, err := studio.FindLauncher(); err != nil {
		fmt.Fprintf(stderr, "FAIL studio launcher: %v\n", err)
		failed = true
	} else {
		fmt.Fprintf(stdout, "ok   studio launcher: %s\n", launcher)
	}
	if cfg, err := loadConfig(*path); err != nil {
		fmt.Fprintf(stderr, "FAIL config: %v\n", err)
		failed = true
	} else {
		fmt.Fprintf(stdout, "ok   config: %s (serves on %s)\n", *path, describeListen(cfg))
	}
	if failed {
		return 1
	}
	return 0
}

func loadConfig(path string) (*config.Config, error) {
	cfg, err := config.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no config at %s; run studiolink-host init", path)
	}
	return cfg, err
}

func runServe(args []string, stderr io.Writer) int {
	fset, path := newFlags("serve", stderr)
	verbose := fset.Bool("v", false, "log every request and StudioMCP's stderr")
	if fset.Parse(args) != nil {
		return 2
	}
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	// Ctrl+C, and on Windows closing the console window, arrive here.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runBridge(ctx, *path, log, "", nil); err != nil {
		log.Error("studiolink-host stopped", "err", err)
		return 1
	}
	return 0
}

func runInterfaces(args []string, stdout, stderr io.Writer) int {
	fset := flag.NewFlagSet("studiolink-host interfaces", flag.ContinueOnError)
	fset.SetOutput(stderr)
	if fset.Parse(args) != nil {
		return 2
	}
	adapters, err := netif.List()
	if err != nil {
		fmt.Fprintf(stderr, "interfaces: %v\n", err)
		return 1
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ADAPTER\tIPv4\tSTATE\tREACHED BY")
	for _, a := range adapters {
		var ips []string
		for _, ip := range a.IPv4 {
			ips = append(ips, ip.String())
		}
		state := "up"
		if !a.Up {
			state = "down"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a.Name, strings.Join(ips, ", "), state, a.Kind())
	}
	_ = tw.Flush()
	fmt.Fprintf(stdout, "\nTo serve on an adapter, add it to studiolink.json by name; the bridge\n"+
		"follows its address when it changes:\n\n"+
		"  \"listen_interfaces\": [{\"name\": \"vEthernet (WSL (Hyper-V firewall))\", \"port\": %d}]\n\n"+
		"Windows Firewall may block other machines until you allow studiolink-host.\n"+
		"For Tailscale peers, prefer the \"tailscale\" setting: it needs no firewall rule.\n", config.DefaultPort)
	return 0
}

// describeListen summarizes where a config serves, for status output.
func describeListen(cfg *config.Config) string {
	return strings.Join(listenParts(cfg), ", ")
}

func listenParts(cfg *config.Config) []string {
	parts := slices.Clone(cfg.Listen)
	for _, li := range cfg.ListenInterfaces {
		parts = append(parts, fmt.Sprintf("adapter %q port %d", li.Name, li.Port))
	}
	if ts := cfg.Tailscale; ts != nil {
		parts = append(parts, fmt.Sprintf("tailnet device %q port %d", ts.DeviceName(), ts.ListenPort()))
	}
	for _, r := range cfg.Relays {
		parts = append(parts, "relay "+r.TunnelURL())
	}
	return parts
}
