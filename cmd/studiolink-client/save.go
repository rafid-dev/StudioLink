package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"studiolink/internal/shim"
	"studiolink/internal/tokenfile"
)

const savedHeader = "# Written by studiolink-client save. Holds the bridge token: keep it private.\n"

// hostOwned reports whether path is the studiolink.env that studiolink-host
// wrote on this machine, which the client must leave alone.
func hostOwned(path string) bool {
	old, err := readFile(path)
	return err == nil && bytes.HasPrefix(old, []byte("# Written by studiolink-host"))
}

// saveEnv writes the client's studiolink.env with the given KEY, value pairs.
func saveEnv(path string, kv ...string) error {
	return tokenfile.WriteValues(path, savedHeader, kv...)
}

// runSave stores the bridge's URL and token in studiolink.env, so connect,
// check and relay need no flags or environment. The token comes from
// -token-file, STUDIOLINK_TOKEN or a hidden prompt, never the command line.
func runSave(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	fset := flag.NewFlagSet("studiolink-client save", flag.ContinueOnError)
	fset.SetOutput(stderr)
	rawURL := fset.String("url", getenv("STUDIOLINK_URL"), "the bridge's /mcp URL (leave out to find it each time)")
	tokenFile := fset.String("token-file", "", "file holding the token (default: STUDIOLINK_TOKEN, else ask)")
	if fset.Parse(args) != nil {
		return 2
	}
	path := savedEnvPath(getenv)
	if path == "" {
		fmt.Fprintln(stderr, "save: no home directory to save in")
		return 1
	}
	if hostOwned(path) {
		fmt.Fprintf(stderr, "save: %s belongs to studiolink-host on this machine; the client already uses it\n", path)
		return 1
	}
	if *rawURL != "" {
		u, err := normalizeBridgeURL(*rawURL)
		if err != nil {
			fmt.Fprintf(stderr, "save: bad -url %q: want http(s)://host:port/mcp (%v)\n", *rawURL, err)
			return 2
		}
		*rawURL = u
	}

	var token string
	var err error
	if *tokenFile != "" || getenv("STUDIOLINK_TOKEN") != "" {
		token, err = readToken(*tokenFile, getenv)
	} else {
		token, err = askToken(stdin, stderr)
	}
	if err != nil {
		fmt.Fprintln(stderr, "save:", err)
		return 2
	}

	// Check before saving, so a typo doesn't get remembered.
	target := *rawURL
	if target == "" {
		if target, err = discoverURL(getenv); err != nil {
			fmt.Fprintln(stderr, "save:", err)
			return 1
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := shim.Probe(ctx, shim.Options{URL: target, Token: token, Log: newLogger(io.Discard, false)})
	if err != nil {
		fmt.Fprintf(stderr, "FAIL %s: %v\nNothing was saved.\n", target, err)
		return 1
	}
	kv := []string{}
	if *rawURL != "" {
		kv = append(kv, "STUDIOLINK_URL", *rawURL)
	}
	kv = append(kv, tokenfile.Key, token)
	if err := saveEnv(path, kv...); err != nil {
		fmt.Fprintln(stderr, "save:", err)
		return 1
	}
	fmt.Fprintf(stdout, "ok   %s\n     %s, MCP %s, %d tools\nSaved to %s\n", target, res.Server, res.Protocol, res.Tools, path)
	fmt.Fprintln(stdout, "Agents can now use: studiolink-client connect")
	return 0
}

// askToken reads the token from the terminal without echoing it, or as a
// line from piped input.
func askToken(stdin io.Reader, stderr io.Writer) (string, error) {
	fmt.Fprint(stderr, "Token (studiolink-host token prints it): ")
	var line string
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return "", err
		}
		line = string(b)
	} else {
		s, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && s == "" {
			return "", errors.New("no token given")
		}
		line = s
	}
	token := strings.TrimSpace(line)
	if token == "" {
		return "", errors.New("no token given")
	}
	return token, nil
}
