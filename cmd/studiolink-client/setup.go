package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"studiolink/internal/shim"
	"studiolink/internal/tokenfile"
	"studiolink/internal/winenv"
)

const (
	clientExe     = "studiolink-client.exe"
	maxTokenAsks  = 5
	healthTimeout = 5 * time.Second
)

// Seams for tests.
var (
	executable  = os.Executable
	addUserPath = winenv.AddUserPath
	lookPath    = exec.LookPath
	// runAgent runs an agent's CLI and returns its combined output.
	runAgent = func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).CombinedOutput()
	}
	// probeBridge checks the token by running a session through the shim.
	probeBridge = func(ctx context.Context, bridgeURL, token string) (*shim.ProbeResult, error) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return shim.Probe(ctx, shim.Options{URL: bridgeURL, Token: token, Log: newLogger(io.Discard, false)})
	}
)

// agent is a CLI that keeps MCP servers in its own config.
type agent struct {
	name, bin, label string
	remove, add      []string // arguments after the binary; add ends with "--"
}

var agents = []agent{
	{"claude", "claude", "Claude Code",
		[]string{"mcp", "remove", "-s", "user", "Roblox_Studio"},
		[]string{"mcp", "add", "-s", "user", "Roblox_Studio", "--"}},
	{"codex", "codex", "Codex",
		[]string{"mcp", "remove", "Roblox_Studio"},
		[]string{"mcp", "add", "Roblox_Studio", "--"}},
}

type setupOptions struct {
	url, tokenFile, dir, agents string
	yes, noInstall              bool
	tty                         *os.File // the console behind stdin, to read the token hidden; nil otherwise
}

// runSetup is "studiolink-client setup" from the command line.
func runSetup(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	fset := flag.NewFlagSet("studiolink-client setup", flag.ContinueOnError)
	fset.SetOutput(stderr)
	var o setupOptions
	fset.StringVar(&o.url, "url", "", "the bridge's /mcp URL (default: find it)")
	fset.StringVar(&o.tokenFile, "token-file", "", "file holding the token (default: STUDIOLINK_TOKEN, else ask)")
	fset.StringVar(&o.dir, "dir", "", `install folder on Windows (default %LOCALAPPDATA%\Programs\StudioLink)`)
	fset.StringVar(&o.agents, "agents", "", "agents to add Studio to: claude, codex or none (default: each one found)")
	fset.BoolVar(&o.yes, "yes", false, "accept the defaults without asking")
	fset.BoolVar(&o.noInstall, "no-install", false, "don't install this exe on Windows; use it where it is")
	if fset.Parse(args) != nil {
		return 2
	}
	o.tty = consoleOf(stdin)
	return setup(o, bufio.NewReader(stdin), stdout, stderr, getenv)
}

// runDoubleClick runs setup for a double-click and keeps the window open
// until Enter is pressed, so the output can be read.
func runDoubleClick(stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	in := bufio.NewReader(stdin)
	code := setup(setupOptions{tty: consoleOf(stdin)}, in, stdout, stderr, getenv)
	fmt.Fprint(stdout, "\nPress Enter to close.")
	_, _ = in.ReadString('\n')
	return code
}

// consoleOf returns r as a file when it is a terminal, else nil.
func consoleOf(r io.Reader) *os.File {
	if f, ok := r.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		return f
	}
	return nil
}

// setup is the guided path for a new machine: find the bridge, get a token
// that works, save both, install this exe on Windows and add Studio to the
// agents found on PATH.
func setup(o setupOptions, in *bufio.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	picked, all, err := parseAgents(o.agents)
	if err != nil {
		fmt.Fprintln(stderr, "setup:", err)
		return 2
	}
	p := &prompter{in: in, out: stdout, yes: o.yes}
	fmt.Fprintln(stdout, "StudioLink client setup")
	fmt.Fprintln(stdout)

	// 1. The bridge.
	bridge, typed, err := findBridge(o, in, stdout, getenv)
	if err != nil {
		fmt.Fprintln(stderr, "setup:", err)
		return 1
	}

	// 2. The token.
	token, saveToken, err := findToken(o, in, stdout, getenv, bridge)
	if err != nil {
		fmt.Fprintln(stderr, "setup:", err)
		return 1
	}

	// 3. Save what was typed or passed in, keeping what is already saved.
	if path := savedEnvPath(getenv); path == "" {
		fmt.Fprintln(stdout, "!  No home folder to save in; nothing was saved")
	} else if hostOwned(path) {
		fmt.Fprintln(stdout, "ok "+path+" belongs to studiolink-host; left as it is")
	} else if saved, err := saveSettings(path, bridge, typed, token, saveToken); err != nil {
		fmt.Fprintln(stderr, "setup:", err)
		return 1
	} else if saved {
		fmt.Fprintln(stdout, "ok Saved to "+path)
	}

	// 4. The exe: on Windows, installed and put on PATH.
	exe, err := executable()
	if err == nil {
		exe, err = filepath.Abs(exe)
	}
	if err != nil {
		fmt.Fprintln(stderr, "setup:", err)
		return 1
	}
	if runtime.GOOS == "windows" && !o.noInstall {
		dir := o.dir
		if dir == "" && getenv("LOCALAPPDATA") != "" {
			dir = filepath.Join(getenv("LOCALAPPDATA"), "Programs", "StudioLink")
		}
		if dir == "" {
			fmt.Fprintln(stderr, "setup: no LOCALAPPDATA; pass -dir")
			return 1
		}
		dst, err := installClient(exe, dir)
		if err != nil {
			fmt.Fprintln(stderr, "setup: install:", err)
			return 1
		}
		exe = dst
		fmt.Fprintln(stdout, "ok Installed in "+dir)
		switch added, err := addUserPath(dir); {
		case err != nil:
			fmt.Fprintln(stdout, "!  Could not add "+dir+" to your PATH: "+err.Error())
		case added:
			fmt.Fprintln(stdout, "ok "+dir+" is on your PATH (new terminals)")
		default:
			fmt.Fprintln(stdout, "ok "+dir+" is already on your PATH")
		}
	} else if runtime.GOOS != "windows" && !onPath(filepath.Dir(exe), getenv("PATH")) {
		fmt.Fprintf(stdout, "!  %s is not on your PATH; to use it everywhere: sudo mv %s /usr/local/bin/\n", filepath.Dir(exe), exe)
	}

	// 5. The agents.
	fmt.Fprintln(stdout)
	setupAgents(exe, picked, all, p, stdout)
	return 0
}

// parseAgents reads -agents: all means every agent found on PATH.
func parseAgents(list string) (picked map[string]bool, all bool, err error) {
	switch list {
	case "":
		return nil, true, nil
	case "none":
		return map[string]bool{}, false, nil
	}
	picked = map[string]bool{}
	for name := range strings.SplitSeq(list, ",") {
		name = strings.ToLower(strings.TrimSpace(name))
		known := false
		for _, a := range agents {
			known = known || a.name == name
		}
		if !known {
			return nil, false, fmt.Errorf("-agents: unknown agent %q (want claude, codex or none)", name)
		}
		picked[name] = true
	}
	return picked, false, nil
}

// findBridge returns the bridge's /mcp URL and whether it came from -url or
// was typed (so it is saved). It takes -url, STUDIOLINK_URL, the saved URL or
// a search, and asks for an address until one answers. With -yes it fails
// instead.
func findBridge(o setupOptions, in *bufio.Reader, stdout io.Writer, getenv func(string) string) (string, bool, error) {
	raw, typed := o.url, o.url != ""
	if raw == "" {
		raw = getenv("STUDIOLINK_URL")
	}
	if raw == "" {
		raw = savedURL(getenv)
	}
	var discoverErr error
	if raw == "" {
		found, err := discoverURL(getenv)
		if err == nil {
			fmt.Fprintln(stdout, "ok Bridge at "+found)
			return found, false, nil
		}
		discoverErr = err
	}
	for {
		if raw != "" {
			u, err := normalizeBridgeURL(raw)
			switch {
			case err != nil:
				fmt.Fprintln(stdout, "!  "+err.Error())
			case bridgeAnswers(u):
				fmt.Fprintln(stdout, "ok Bridge at "+u)
				return u, typed, nil
			default:
				fmt.Fprintln(stdout, "!  no StudioLink bridge answers at "+u+"; is the bridge serving this network, and is it allowed through Windows Firewall?")
			}
		}
		if o.yes {
			if discoverErr != nil {
				return "", false, discoverErr
			}
			return "", false, errors.New("no StudioLink bridge answers: pass -url or set STUDIOLINK_URL")
		}
		addr, err := askLine(in, stdout, "Bridge address (shown in StudioLink settings on the Studio PC): ")
		if err != nil {
			return "", false, err
		}
		raw, typed = addr, true
	}
}

// bridgeAnswers reports whether the bridge's /healthz answers at mcpURL.
func bridgeAnswers(mcpURL string) bool {
	u, err := url.Parse(mcpURL)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), healthTimeout)
	defer cancel()
	return probeHealth(ctx, u.Scheme+"://"+u.Host)
}

// normalizeBridgeURL turns what someone types into the bridge's /mcp URL:
// a scheme defaults to http, and the port to 44770 for http only.
func normalizeBridgeURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("no address given")
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("bad address: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("scheme %q: want http or https", u.Scheme)
	}
	if u.Hostname() == "" {
		return "", errors.New("no host in the address")
	}
	if u.Scheme == "http" && u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), strconv.Itoa(defaultPort))
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/mcp"
	}
	return u.String(), nil
}

// findToken returns a token the bridge accepts, and whether it should be
// saved (it was given, not read from a saved file). Without one that works,
// it asks, up to maxTokenAsks times.
func findToken(o setupOptions, in *bufio.Reader, stdout io.Writer, getenv func(string) string, bridge string) (string, bool, error) {
	// Tokens from an env file are already saved, so they aren't saved again.
	save := o.tokenFile != "" || strings.TrimSpace(getenv("STUDIOLINK_TOKEN")) != ""
	token, err := readToken(o.tokenFile, getenv)
	if err != nil {
		if o.tokenFile != "" {
			fmt.Fprintln(stdout, "!  "+err.Error())
		}
		token = ""
	}
	asked := 0
	for {
		if token != "" {
			res, err := probeBridge(context.Background(), bridge, token)
			if err == nil {
				fmt.Fprintf(stdout, "ok Token accepted: %s, %d tools\n", res.Server, res.Tools)
				return token, save, nil
			}
			if !strings.Contains(err.Error(), "rejected the token") {
				return "", false, err
			}
			fmt.Fprintln(stdout, "!  The bridge refused that token.")
			token = ""
		}
		if o.yes {
			return "", false, errors.New("no working token: set STUDIOLINK_TOKEN, pass -token-file, or run setup without -yes")
		}
		if asked == maxTokenAsks {
			return "", false, errors.New("too many wrong tokens")
		}
		asked++
		typed, err := promptToken(in, o.tty, stdout)
		if err != nil {
			return "", false, err
		}
		token, save = typed, true
	}
}

// promptToken reads the token hidden when stdin is a console. Input already
// buffered in in is read as a line, so nothing typed ahead is lost.
func promptToken(in *bufio.Reader, tty *os.File, stdout io.Writer) (string, error) {
	fmt.Fprint(stdout, "Token (from StudioLink settings on the Studio PC; it won't be shown): ")
	var line string
	if tty != nil && in.Buffered() == 0 {
		b, err := term.ReadPassword(int(tty.Fd()))
		fmt.Fprintln(stdout)
		if err != nil {
			return "", err
		}
		line = string(b)
	} else {
		s, err := in.ReadString('\n')
		fmt.Fprintln(stdout) // piped input is not echoed, so end the line
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

// saveSettings writes the URL (when typed) and token (when save is set) into
// the saved env file, keeping what it already holds. It reports whether the
// file changed.
func saveSettings(path, bridge string, typed bool, token string, save bool) (bool, error) {
	data, _ := readFile(path)
	old := tokenfile.Values(data)
	bridgeURL := old["STUDIOLINK_URL"]
	if typed {
		bridgeURL = bridge
	}
	tok := old[tokenfile.Key]
	if save {
		tok = token
	}
	if bridgeURL == old["STUDIOLINK_URL"] && tok == old[tokenfile.Key] {
		return false, nil
	}
	var kv []string
	if bridgeURL != "" {
		kv = append(kv, "STUDIOLINK_URL", bridgeURL)
	}
	if tok != "" {
		kv = append(kv, tokenfile.Key, tok)
	}
	if err := saveEnv(path, kv...); err != nil {
		return false, err
	}
	return true, nil
}

// installClient copies exe to dir\studiolink-client.exe unless it is that
// file already. It returns the installed path.
func installClient(exe, dir string) (string, error) {
	dst := filepath.Join(dir, clientExe)
	if sameFile(exe, dst) {
		return dst, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := copyFile(exe, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// copyFile copies src to dst through a temporary file, so a failed copy
// never leaves a half-written binary behind.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// sameFile reports whether a and b name the same file.
func sameFile(a, b string) bool {
	ia, err1 := os.Stat(a)
	ib, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(ia, ib)
}

// onPath reports whether dir is one of the entries of the PATH list.
func onPath(dir, list string) bool {
	for entry := range strings.SplitSeq(list, string(filepath.ListSeparator)) {
		if entry != "" && filepath.Clean(entry) == filepath.Clean(dir) {
			return true
		}
	}
	return false
}

// setupAgents adds Studio to each agent that is picked (or found, when all)
// and on PATH, after asking. The client path is what the agents will run.
func setupAgents(client string, picked map[string]bool, all bool, p *prompter, stdout io.Writer) {
	var todo []agent
	for _, a := range agents {
		if !all && !picked[a.name] {
			continue
		}
		if _, err := lookPath(a.bin); err != nil {
			if !all {
				fmt.Fprintf(stdout, "!  %s is not on your PATH\n", a.bin)
			}
			continue
		}
		todo = append(todo, a)
	}
	if len(todo) == 0 {
		fmt.Fprintln(stdout, "Studio isn't added to an agent. Run one of these yourself:")
		fmt.Fprintf(stdout, "  claude mcp add -s user Roblox_Studio -- %s connect\n", client)
		fmt.Fprintf(stdout, "  codex mcp add Roblox_Studio -- %s connect\n", client)
		return
	}
	for _, a := range todo {
		if !p.ask("Add Roblox Studio to "+a.label+"?", true) {
			continue
		}
		// The remove fails when there is nothing to replace; that is fine.
		_, _ = runAgent(a.bin, a.remove...)
		add := append(append([]string{}, a.add...), client, "connect")
		if out, err := runAgent(a.bin, add...); err != nil {
			fmt.Fprintf(stdout, "!  Could not add to %s: %v\n%s\n", a.label, err, strings.TrimSpace(string(out)))
			continue
		}
		fmt.Fprintln(stdout, "ok Added to "+a.label)
	}
	fmt.Fprintln(stdout, "Done. Restart your agent to see the Studio tools.")
}

// askLine prints prompt and returns the next non-empty line.
func askLine(in *bufio.Reader, out io.Writer, prompt string) (string, error) {
	for {
		fmt.Fprint(out, prompt)
		line, err := in.ReadString('\n')
		if s := strings.TrimSpace(line); s != "" {
			return s, nil
		}
		if err != nil {
			return "", errors.New("no answer given")
		}
	}
}

// prompter asks yes/no questions. With yes set, or once input runs out, it
// takes each question's default.
type prompter struct {
	in  *bufio.Reader
	out io.Writer
	yes bool
}

func (p *prompter) ask(question string, def bool) bool {
	hint := "[Y/n]"
	if !def {
		hint = "[y/N]"
	}
	if p.yes {
		fmt.Fprintf(p.out, "%s %s %s\n", question, hint, answer(def))
		return def
	}
	for {
		fmt.Fprintf(p.out, "%s %s ", question, hint)
		line, err := p.in.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return true
		case "n", "no":
			return false
		case "":
			if err != nil {
				fmt.Fprintln(p.out, answer(def))
			}
			return def
		}
		if err != nil {
			fmt.Fprintln(p.out)
			return def
		}
	}
}

func answer(b bool) string {
	if b {
		return "y"
	}
	return "n"
}
