package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"studiolink/internal/tokenfile"
)

// The client finds the bridge and its token by itself where it can, so an
// agent's config can be just "studiolink-client connect".

const defaultPort = 44770

// Seams for tests.
var (
	readFile = os.ReadFile
	glob     = filepath.Glob
	// probeHealth reports whether a bridge (or relay) answers at base.
	probeHealth = func(ctx context.Context, base string) bool {
		req, err := http.NewRequestWithContext(ctx, "GET", base+"/healthz", nil)
		if err != nil {
			return false
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 128))
		// A relay whose Studio PC hasn't connected yet still counts: the
		// agent then gets the relay's own explanation.
		return (resp.StatusCode == http.StatusOK && strings.HasPrefix(string(body), "ok")) ||
			(resp.StatusCode == http.StatusServiceUnavailable && strings.HasPrefix(string(body), "waiting for studiolink-host"))
	}
)

// configDir is where the client keeps studiolink.env: ~/.config/StudioLink
// (or XDG_CONFIG_HOME), the macOS equivalent, or %APPDATA%\StudioLink, which
// on the Studio PC is the bridge's own folder.
func configDir(getenv func(string) string) string {
	switch runtime.GOOS {
	case "windows":
		if d := getenv("APPDATA"); d != "" {
			return filepath.Join(d, "StudioLink")
		}
	case "darwin":
		if h := getenv("HOME"); h != "" {
			return filepath.Join(h, "Library", "Application Support", "StudioLink")
		}
	default:
		if d := getenv("XDG_CONFIG_HOME"); d != "" {
			return filepath.Join(d, "StudioLink")
		}
		if h := getenv("HOME"); h != "" {
			return filepath.Join(h, ".config", "StudioLink")
		}
	}
	return ""
}

// savedEnvPath is the studiolink.env that "save" writes, or "".
func savedEnvPath(getenv func(string) string) string {
	if d := configDir(getenv); d != "" {
		return filepath.Join(d, tokenfile.Name)
	}
	return ""
}

func inWSL(getenv func(string) string) bool {
	return runtime.GOOS == "linux" && (getenv("WSL_DISTRO_NAME") != "" || getenv("WSL_INTEROP") != "")
}

func inContainer() bool {
	for _, f := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}
	return false
}

// envFiles lists the env files that may hold the token and URL, best first.
func envFiles(getenv func(string) string) []string {
	var files []string
	if f := getenv("STUDIOLINK_TOKEN_FILE"); f != "" {
		files = append(files, f)
	}
	if runtime.GOOS != "windows" {
		files = append(files, "/run/secrets/studiolink_token")
	}
	if f := savedEnvPath(getenv); f != "" {
		files = append(files, f)
	}
	// In WSL, the bridge's own file on the Windows side, when there is
	// exactly one Windows user with it.
	if inWSL(getenv) {
		if m, _ := glob("/mnt/c/Users/*/AppData/Roaming/StudioLink/" + tokenfile.Name); len(m) == 1 {
			files = append(files, m[0])
		}
	}
	return files
}

// readToken finds the token: -token-file, STUDIOLINK_TOKEN, then the env
// files in envFiles order.
func readToken(tokenFile string, getenv func(string) string) (string, error) {
	if tokenFile != "" {
		token, err := tokenfile.Read(tokenFile)
		if err != nil {
			return "", fmt.Errorf("token file: %w", err)
		}
		return token, nil
	}
	if token := strings.TrimSpace(getenv("STUDIOLINK_TOKEN")); token != "" {
		return token, nil
	}
	for _, f := range envFiles(getenv) {
		data, err := readFile(f)
		if err != nil {
			continue
		}
		if token, err := tokenfile.Parse(data); err == nil {
			return token, nil
		}
	}
	return "", errors.New("no token: set STUDIOLINK_TOKEN, pass -token-file, or run studiolink-client save (studiolink-host token prints it)")
}

// savedURL returns STUDIOLINK_URL from the saved env file, or "".
func savedURL(getenv func(string) string) string {
	for _, f := range envFiles(getenv) {
		if data, err := readFile(f); err == nil {
			if u := tokenfile.Values(data)["STUDIOLINK_URL"]; u != "" {
				return u
			}
		}
	}
	return ""
}

// urlCandidates are the places a bridge may be, best first.
func urlCandidates(getenv func(string) string) []string {
	var hosts []string
	switch {
	case inContainer():
		hosts = append(hosts, "host.docker.internal")
	case inWSL(getenv):
		// Mirrored networking shares Windows' loopback; NAT mode reaches
		// Windows at the default gateway.
		hosts = append(hosts, "127.0.0.1")
		if gw := defaultGateway(); gw != "" {
			hosts = append(hosts, gw)
		}
	}
	hosts = append(hosts, "127.0.0.1")
	var urls []string
	seen := map[string]bool{}
	for _, h := range hosts {
		u := "http://" + net.JoinHostPort(h, strconv.Itoa(defaultPort))
		if !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}
	return urls
}

// discoverURL returns the first candidate with a bridge answering.
func discoverURL(getenv func(string) string) (string, error) {
	candidates := urlCandidates(getenv)
	for _, base := range candidates {
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		ok := probeHealth(ctx, base)
		cancel()
		if ok {
			return base + "/mcp", nil
		}
	}
	return "", fmt.Errorf("no bridge found at %s: pass -url, set STUDIOLINK_URL, or run studiolink-client save -url URL",
		strings.Join(candidates, ", "))
}

// defaultGateway reads the IPv4 default route from /proc/net/route.
func defaultGateway() string {
	data, err := readFile("/proc/net/route")
	if err != nil {
		return ""
	}
	return parseGateway(string(data))
}

func parseGateway(table string) string {
	for line := range strings.SplitSeq(table, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || f[1] != "00000000" {
			continue
		}
		b, err := hex.DecodeString(f[2])
		if err != nil || len(b) != 4 {
			continue
		}
		ip := make(net.IP, 4)
		binary.BigEndian.PutUint32(ip, binary.LittleEndian.Uint32(b))
		if !ip.IsUnspecified() {
			return ip.String()
		}
	}
	return ""
}
