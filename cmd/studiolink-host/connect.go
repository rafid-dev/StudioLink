package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"studiolink/internal/config"
	"studiolink/internal/netif"
	"studiolink/internal/tokenfile"
)

// scenarios are the places an agent can run, with what connect prints for
// each.
var scenarios = []struct {
	name, what string
	print      func(*recipes, io.Writer)
}{
	{"docker", "Docker containers on this PC", (*recipes).docker},
	{"wsl", "WSL2 distros", (*recipes).wsl},
	{"vm", "Hyper-V, VMware or VirtualBox VMs", (*recipes).vm},
	{"tailscale", "other machines on your tailnet", (*recipes).tailscale},
	{"server", "a VPS or cloud server (dial-out through a relay)", (*recipes).server},
}

// recipes fills in the commands for one config.
type recipes struct {
	cfg        *config.Config
	envPath    string // studiolink.env
	adapters   []netif.Adapter
	tailnetURL string // the running bridge's, if any
	downloads  downloads
	mirrored   bool // WSL2 shares this PC's loopback
}

func runConnect(args []string, stdout, stderr io.Writer) int {
	fset, path := newFlags("connect", stderr)
	fset.Usage = func() {
		fmt.Fprintln(stderr, "usage: studiolink-host connect [-config PATH] [docker|wsl|vm|tailscale|server]")
		fset.PrintDefaults()
	}
	if fset.Parse(args) != nil {
		return 2
	}
	abs, err := filepath.Abs(*path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	cfg, err := loadConfig(abs)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	r := &recipes{cfg: cfg, envPath: tokenfile.PathFor(abs), downloads: newDownloads(), mirrored: wslMirrored()}
	r.adapters, _ = netif.List()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if st, err := fetchStatus(ctx, abs, cfg.Token); err == nil {
		r.tailnetURL = st.Tailnet
	} else {
		fmt.Fprintln(stdout, "Note: the bridge is not running; start it with \"studiolink-host start\".")
		fmt.Fprintln(stdout)
	}
	// The env file is how containers get the token; make sure it exists even
	// before the first start.
	_ = tokenfile.Write(r.envPath, cfg.Token)

	name := fset.Arg(0)
	if name == "" {
		fmt.Fprintln(stdout, "Where does your agent run?")
		fmt.Fprintln(stdout)
		for _, s := range scenarios {
			fmt.Fprintf(stdout, "  studiolink-host connect %-10s %s\n", s.name, s.what)
		}
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "Claude Code on this PC itself needs no bridge: add Studio's MCP server directly.")
		return 0
	}
	for _, s := range scenarios {
		if s.name == name {
			s.print(r, stdout)
			fmt.Fprintln(stdout)
			fmt.Fprintln(stdout, "These commands contain your token: don't paste them into chats or commit them.")
			return 0
		}
	}
	fset.Usage()
	return 2
}

// loopbackPort is the port the bridge serves on this PC's loopback, or 0.
func (r *recipes) loopbackPort() int { return loopbackPort(r.cfg) }

func loopbackPort(cfg *config.Config) int {
	for _, addr := range cfg.Listen {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			continue
		}
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
			p, _ := strconv.Atoi(port)
			return p
		}
	}
	return 0
}

// served returns the configured adapters matching keep, with their current
// address.
func (r *recipes) served(keep func(netif.Adapter) bool) []string {
	var urls []string
	for _, li := range r.cfg.ListenInterfaces {
		for _, a := range r.adapters {
			if strings.EqualFold(a.Name, li.Name) && keep(a) && len(a.IPv4) > 0 {
				urls = append(urls, fmt.Sprintf("http://%s:%d", a.IPv4[0], li.Port))
			}
		}
	}
	return urls
}

func isWSL(a netif.Adapter) bool { return strings.Contains(strings.ToLower(a.Name), "(wsl") }

func (r *recipes) header() string { return `"Authorization: Bearer ` + r.cfg.Token + `"` }

// installLine is how an agent's machine gets studiolink-client from base.
func (r *recipes) installLine(base string) string {
	if r.downloads.available() {
		return "curl -fsSL " + base + "/install.sh | sh"
	}
	return "(this is a dev build without client downloads: build it with GOOS=linux go build ./cmd/studiolink-client)"
}

func (r *recipes) docker(w io.Writer) {
	port := r.loopbackPort()
	if port == 0 {
		fmt.Fprintf(w, "Containers reach this PC's loopback, but the bridge doesn't serve there. Add\n  \"listen\": [\"127.0.0.1:%d\"]\nto the config, then run studiolink-host stop and studiolink-host start.\n", config.DefaultPort)
		return
	}
	base := fmt.Sprintf("http://host.docker.internal:%d", port)
	fmt.Fprintf(w, `Docker containers on this PC reach the bridge at %[1]s/mcp

Claude Code, once per container (or per volume that keeps ~/.claude.json):
  claude mcp add --transport http -s user Roblox_Studio %[1]s/mcp --header %[2]s

Or, without copying the token, hand the container StudioLink's token file:
  docker run --env-file "%[3]s" ...
  compose.yaml, under the service:   env_file: ${APPDATA}/StudioLink/%[4]s
and point .mcp.json at it (Claude Code fills in ${STUDIOLINK_TOKEN}):
  {"mcpServers": {"Roblox_Studio": {"type": "http", "url": "%[1]s/mcp",
    "headers": {"Authorization": "Bearer ${STUDIOLINK_TOKEN}"}}}}

Agents that only run stdio MCP servers: in the container,
  %[5]s
then use "studiolink-client connect" as the server command. It finds the
bridge by itself and reads STUDIOLINK_TOKEN.
`, base, r.header(), r.envPath, tokenfile.Name, r.installLine(base))
}

func (r *recipes) wsl(w io.Writer) {
	var base string
	switch urls := r.served(isWSL); {
	case r.mirrored && r.loopbackPort() != 0:
		base = fmt.Sprintf("http://127.0.0.1:%d", r.loopbackPort())
	case len(urls) > 0:
		base = urls[0]
	default:
		fmt.Fprintln(w, "WSL2 can't reach the bridge yet. Run \"studiolink-host setup\" and let it serve")
		fmt.Fprintln(w, "WSL2, or add the WSL adapter from \"studiolink-host interfaces\" to listen_interfaces.")
		return
	}
	fmt.Fprintf(w, `WSL2 distros reach the bridge at %[1]s/mcp

In your distro:
  %[2]s
  studiolink-client check
  claude mcp add -s user Roblox_Studio -- studiolink-client connect

studiolink-client finds the bridge (the address changes when Windows
restarts) and reads the token from this PC's %[3]s, so
there is nothing to copy.
`, base, r.installLine(base), tokenfile.Name)
}

func (r *recipes) vm(w io.Writer) {
	urls := r.served(func(a netif.Adapter) bool { return !isWSL(a) })
	if len(urls) == 0 {
		fmt.Fprintln(w, "VMs can't reach the bridge yet. \"studiolink-host interfaces\" lists the adapters")
		fmt.Fprintln(w, "your VMs use; add one to listen_interfaces in the config, for example")
		fmt.Fprintf(w, "  \"listen_interfaces\": [{\"name\": \"vEthernet (Default Switch)\", \"port\": %d}]\n", config.DefaultPort)
		fmt.Fprintln(w, "then run studiolink-host stop and studiolink-host start, and this again.")
		return
	}
	fmt.Fprintf(w, "VMs reach the bridge at %s\n", strings.Join(urls, " or "))
	base := urls[0]
	fmt.Fprintf(w, `
Claude Code in the VM:
  claude mcp add --transport http -s user Roblox_Studio %[1]s/mcp --header %[2]s

%[3]s
Windows Firewall may block the VM until you allow studiolink-host.
`, base, r.header(), r.stdioSteps(base, "Stdio-only agents (Linux or macOS VM)"))
}

// stdioSteps installs studiolink-client from base and saves the URL and
// token, so the agent's command is just "studiolink-client connect".
func (r *recipes) stdioSteps(base, who string) string {
	return fmt.Sprintf(`%s:
  %s
  studiolink-client save -url %s/mcp
  (paste this token when it asks: %s)
  claude mcp add -s user Roblox_Studio -- studiolink-client connect
`, who, r.installLine(base), base, r.cfg.Token)
}

func (r *recipes) tailscale(w io.Writer) {
	ts := r.cfg.Tailscale
	if ts == nil {
		fmt.Fprintln(w, "The bridge isn't on your tailnet yet. Add this to the config:")
		fmt.Fprintln(w, `  "tailscale": {"hostname": "studiolink"}`)
		fmt.Fprintln(w, "then run studiolink-host stop and studiolink-host start, open the login URL")
		fmt.Fprintln(w, "from the log (tray menu: Open log) once, and run this again.")
		return
	}
	base := strings.TrimSuffix(r.tailnetURL, "/mcp")
	if base == "" {
		scheme := "http"
		if ts.HTTPS {
			scheme = "https"
		}
		base = fmt.Sprintf("%s://%s.<your-tailnet>.ts.net", scheme, ts.DeviceName())
		fmt.Fprintln(w, "(Start the bridge and log in to Tailscale for the exact address.)")
	}
	fmt.Fprintf(w, `Tailnet peers reach the bridge at %[1]s/mcp

Claude Code on the peer:
  claude mcp add --transport http -s user Roblox_Studio %[1]s/mcp --header %[2]s

%[3]s`, base, r.header(), r.stdioSteps(base, "Stdio-only agents (Linux or macOS)"))
}

func (r *recipes) server(w io.Writer) {
	if len(r.cfg.Relays) > 0 {
		fmt.Fprintf(w, `This PC dials out to %[1]s.

On that server, agents reach the bridge at http://127.0.0.1:44770/mcp:
  claude mcp add --transport http -s user Roblox_Studio http://127.0.0.1:44770/mcp --header %[2]s

Stdio-only agents on the server (the relay's own studiolink-client):
  studiolink-client save
  (paste this token when it asks: %[3]s)
  claude mcp add -s user Roblox_Studio -- studiolink-client connect
`, r.cfg.Relays[0].URL, r.header(), r.cfg.Token)
		return
	}
	client := filepath.Join(r.downloads.dir, clientFile("linux-amd64"))
	if !r.downloads.available() {
		client = "studiolink-client (build it with GOOS=linux go build ./cmd/studiolink-client)"
	}
	fmt.Fprintf(w, `A server can't reach this PC, so this PC dials out to a relay on it.

1. Copy the client to the server (arm64 servers: studiolink-client-linux-arm64):
     scp "%[1]s" you@server:studiolink-client

2. On the server, open TCP port 44771 and start the relay:
     chmod +x studiolink-client
     STUDIOLINK_TOKEN=%[2]s ./studiolink-client relay
   It prints a command with its certificate's fingerprint.

3. Run that command here, with the server's address in it:
     studiolink-host add-relay wss://<server>:44771 sha256:...

Then run "studiolink-host connect server" again for the agent's side.
To run the relay in Docker instead, see docs/guides/server.md.
`, client, r.cfg.Token)
}

// wslMirrored reports whether WSL2 runs in mirrored networking mode, where
// distros reach this PC's loopback directly.
func wslMirrored() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	data, err := os.ReadFile(filepath.Join(home, ".wslconfig"))
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok && strings.EqualFold(strings.TrimSpace(key), "networkingMode") &&
			strings.EqualFold(strings.TrimSpace(value), "mirrored") {
			return true
		}
	}
	return false
}
