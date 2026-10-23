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
	relay := config.Relay{URL: fset.Arg(0), Fingerprint: fset.Arg(1)}
	if !strings.Contains(relay.URL, "://") {
		relay.URL = "wss://" + relay.URL
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
	replaced := false
	for i, r := range cfg.Relays {
		if r.TunnelURL() == relay.TunnelURL() {
			cfg.Relays[i] = relay
			replaced = true
		}
	}
	if !replaced {
		cfg.Relays = append(cfg.Relays, relay)
	}
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
