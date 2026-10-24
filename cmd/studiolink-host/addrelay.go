package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"studiolink/internal/config"
)

// runAddRelay adds (or updates) a relay in the config and restarts a
// running bridge so it dials out at once. The relay prints this command
// with its fingerprint filled in.
func runAddRelay(args []string, stdout, stderr io.Writer) int {
	fset, path := newFlags("add-relay", stderr)
	fset.Usage = func() {
		fmt.Fprintln(stderr, "usage: studiolink-host add-relay [-config PATH] wss://server:44771 [sha256:FINGERPRINT]")
		fset.PrintDefaults()
	}
	if fset.Parse(args) != nil {
		return 2
	}
	if fset.NArg() < 1 || fset.NArg() > 2 {
		fset.Usage()
		return 2
	}
	relay := newRelay(fset.Arg(0), fset.Arg(1))
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
	replaced := mergeRelay(cfg, relay)
	if err := cfg.Save(abs); err != nil {
		fmt.Fprintln(stderr, "add-relay:", err)
		return 1
	}
	if replaced {
		fmt.Fprintf(stdout, "Updated the relay %s in %s.\n", relay.URL, abs)
	} else {
		fmt.Fprintf(stdout, "Added the relay %s to %s.\n", relay.URL, abs)
	}
	if code := restartIfRunning(abs, cfg.Token, stdout, stderr); code != 0 {
		return code
	}
	fmt.Fprintln(stdout, "Next: studiolink-host connect server")
	return 0
}

// newRelay makes a relay from a URL and an optional fingerprint, adding
// wss:// when the URL has no scheme.
func newRelay(rawURL, fingerprint string) config.Relay {
	relay := config.Relay{URL: rawURL, Fingerprint: fingerprint}
	if !strings.Contains(relay.URL, "://") {
		relay.URL = "wss://" + relay.URL
	}
	return relay
}

// parseRelayCommand reads the line a relay prints, which may start with
// "studiolink-host add-relay", and returns the relay it names. A line with
// just the URL and fingerprint, or just the URL, works too.
func parseRelayCommand(line string) (config.Relay, error) {
	fields := strings.Fields(line)
	if len(fields) > 0 && strings.TrimSuffix(strings.ToLower(fields[0]), ".exe") == "studiolink-host" {
		fields = fields[1:]
	}
	if len(fields) > 0 && fields[0] == "add-relay" {
		fields = fields[1:]
	}
	switch len(fields) {
	case 1:
		return newRelay(fields[0], ""), nil
	case 2:
		return newRelay(fields[0], fields[1]), nil
	case 0:
		return config.Relay{}, errors.New("paste the add-relay command your server printed")
	default:
		return config.Relay{}, errors.New("want: studiolink-host add-relay wss://server:44771 sha256:FINGERPRINT")
	}
}

// mergeRelay adds relay to cfg, or replaces the relay with the same tunnel
// address. It reports whether one was replaced.
func mergeRelay(cfg *config.Config, relay config.Relay) bool {
	for i, r := range cfg.Relays {
		if r.TunnelURL() == relay.TunnelURL() {
			cfg.Relays[i] = relay
			return true
		}
	}
	cfg.Relays = append(cfg.Relays, relay)
	return false
}

// restartIfRunning restarts the bridge for the config at abs so it picks
// up changes; it does nothing when none is running.
func restartIfRunning(abs, token string, stdout, stderr io.Writer) int {
	switch err := stopBridge(context.Background(), abs, token); {
	case errors.Is(err, errNotRunning):
		fmt.Fprintln(stdout, "The bridge is not running; it uses the change when you start it.")
		return 0
	case err != nil:
		fmt.Fprintln(stderr, "cannot restart the bridge:", err)
		return 1
	}
	fmt.Fprintln(stdout, "Restarting the bridge...")
	return runStart([]string{"-config", abs}, stdout, stderr)
}
