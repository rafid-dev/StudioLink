// Command studiolink-client runs next to the AI agent (in a VM, container or on a
// remote server). It gives stdio-only agents a local MCP server and, in
// dial-out mode, accepts the tunnel that studiolink-host opens.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"studiolink/internal/shim"
	"studiolink/internal/version"
)

const usage = `studiolink-client: connect an AI agent to a remote studiolink-host.

Usage:
  studiolink-client <command> [flags]

Commands:
  connect   be a stdio MCP server that forwards to studiolink-host
            (this is the command to give your agent)
  check     test the connection and print what the bridge offers
  version   print the version

Both connect and check take:
  -url URL          the bridge's /mcp URL (or STUDIOLINK_URL)
  -token-file PATH  file holding the token (or STUDIOLINK_TOKEN)

Example, Claude Code in a container:
  claude mcp add Roblox_Studio -e STUDIOLINK_TOKEN=<token> -- \
    studiolink-client connect -url http://host.docker.internal:44770/mcp
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "connect":
		return runConnect(args[1:], stdin, stdout, stderr, getenv)
	case "check":
		return runCheck(args[1:], stdout, stderr, getenv)
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

// target is where to connect and with what token.
type target struct {
	url, token string
}

// parseTarget reads -url and -token-file (or their environment variables).
// There is deliberately no -token flag: command lines are visible to other
// users and end up in agent config files and logs.
func parseTarget(name string, args []string, stderr io.Writer, getenv func(string) string) (*target, *bool, error) {
	fset := flag.NewFlagSet("studiolink-client "+name, flag.ContinueOnError)
	fset.SetOutput(stderr)
	rawURL := fset.String("url", getenv("STUDIOLINK_URL"), "the bridge's /mcp URL")
	tokenFile := fset.String("token-file", "", "file holding the token (default: STUDIOLINK_TOKEN)")
	verbose := fset.Bool("v", false, "debug logging (to stderr)")
	if err := fset.Parse(args); err != nil {
		return nil, nil, err
	}
	if *rawURL == "" {
		return nil, nil, errors.New("no bridge URL: pass -url or set STUDIOLINK_URL")
	}
	u, err := url.Parse(*rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, nil, fmt.Errorf("bad -url %q: want http(s)://host:port/mcp", *rawURL)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/mcp"
	}
	token := strings.TrimSpace(getenv("STUDIOLINK_TOKEN"))
	if *tokenFile != "" {
		b, err := os.ReadFile(*tokenFile)
		if err != nil {
			return nil, nil, fmt.Errorf("token file: %w", err)
		}
		token = strings.TrimSpace(string(b))
	}
	if token == "" {
		return nil, nil, errors.New("no token: set STUDIOLINK_TOKEN or pass -token-file (studiolink-host token prints it)")
	}
	return &target{url: u.String(), token: token}, verbose, nil
}

func newLogger(stderr io.Writer, verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))
}

func runConnect(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	t, verbose, err := parseTarget("connect", args, stderr, getenv)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stderr, "connect:", err)
		}
		return 2
	}
	// stdout belongs to the MCP protocol; every log line goes to stderr.
	log := newLogger(stderr, *verbose)
	warnIfPlaintext(log, t.url)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := shim.Run(ctx, shim.Options{URL: t.url, Token: t.token, Log: log}, stdin, stdout); err != nil {
		log.Error("stopped", "err", err)
		return 1
	}
	return 0
}

func runCheck(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	t, verbose, err := parseTarget("check", args, stderr, getenv)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stderr, "check:", err)
		}
		return 2
	}
	log := newLogger(stderr, *verbose)
	warnIfPlaintext(log, t.url)
	if !*verbose {
		// The FAIL line below already reports what the shim would warn about.
		log = slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := shim.Probe(ctx, shim.Options{URL: t.url, Token: t.token, Log: log})
	if err != nil {
		fmt.Fprintf(stderr, "FAIL %s: %v\n", t.url, err)
		return 1
	}
	fmt.Fprintf(stdout, "ok   %s\n     %s, MCP %s, %d tools\n", t.url, res.Server, res.Protocol, res.Tools)
	return 0
}

// warnIfPlaintext warns when the token would cross a network that is neither
// local nor encrypted by Tailscale.
func warnIfPlaintext(log *slog.Logger, raw string) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || isTrustedHost(u.Hostname()) {
		return
	}
	log.Warn("the token is sent unencrypted to this host; use https, Tailscale or a private network", "url", raw)
}

var tailscaleRange = netip.MustParsePrefix("100.64.0.0/10")

func isTrustedHost(host string) bool {
	host = strings.ToLower(host)
	switch {
	case host == "localhost", host == "host.docker.internal",
		strings.HasSuffix(host, ".ts.net"), strings.HasSuffix(host, ".internal"),
		strings.HasSuffix(host, ".local"), !strings.Contains(host, "."):
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || tailscaleRange.Contains(ip))
}
