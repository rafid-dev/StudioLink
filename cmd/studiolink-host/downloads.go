package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// clientPlatforms are the studiolink-client builds a release puts in the
// clients folder next to studiolink-host.exe, as GOOS-GOARCH.
var clientPlatforms = []string{
	"linux-amd64", "linux-arm64", "darwin-arm64", "darwin-amd64", "windows-amd64", "windows-arm64",
}

// clientFile is the download name for a platform.
func clientFile(platform string) string {
	name := "studiolink-client-" + platform
	if strings.HasPrefix(platform, "windows-") {
		name += ".exe"
	}
	return name
}

// downloads serves the client binaries and an install script, so a VM,
// container or Tailscale peer that can reach the bridge installs the client
// with one curl and no other download. They hold nothing secret, so they need
// no token.
type downloads struct {
	dir string // the clients folder; empty or missing in a dev build
}

func newDownloads() downloads {
	return downloads{dir: filepath.Join(filepath.Dir(executable()), "clients")}
}

// available reports whether this build ships client binaries.
func (d downloads) available() bool {
	_, err := os.Stat(filepath.Join(d.dir, clientFile("linux-amd64")))
	return err == nil
}

// wrap adds /install.sh and /download/ in front of the MCP handler.
func (d downloads) wrap(next http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", next)
	mux.HandleFunc("GET /install.sh", d.serveScript)
	mux.HandleFunc("GET /download/{name}", d.serveFile)
	return mux
}

func (d downloads) serveFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !slices.ContainsFunc(clientPlatforms, func(p string) bool { return clientFile(p) == name }) {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(d.dir, name))
	if err != nil {
		http.Error(w, "this studiolink-host has no "+name+"; use a release build", http.StatusNotFound)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	if info, err := f.Stat(); err == nil {
		http.ServeContent(w, r, name, info.ModTime(), f)
		return
	}
	_, _ = io.Copy(w, f)
}

func (d downloads) serveScript(w http.ResponseWriter, r *http.Request) {
	if !d.available() {
		http.Error(w, "echo 'this studiolink-host has no client binaries; use a release build' >&2; exit 1", http.StatusNotFound)
		return
	}
	// The host ends up in a script run by sh; refuse anything odd in it.
	if r.Host == "" || strings.Trim(r.Host, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.-:[]") != "" {
		http.Error(w, "bad Host header", http.StatusBadRequest)
		return
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	_, _ = io.WriteString(w, installScript(scheme+"://"+r.Host))
}

// installScript downloads the client for this machine from base and puts it
// on PATH. It is POSIX sh, for Linux and macOS.
func installScript(base string) string {
	return strings.ReplaceAll(`#!/bin/sh
# Installs studiolink-client from the studiolink-host at BASE.
#   curl -fsSL BASE/install.sh | sh
# Set STUDIOLINK_BIN to choose the folder (default /usr/local/bin as root,
# else ~/.local/bin).
set -eu
base='BASE'
case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) echo "studiolink: unsupported OS $(uname -s); on Windows download $base/download/studiolink-client-windows-amd64.exe" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "studiolink: unsupported CPU $(uname -m)" >&2; exit 1 ;;
esac
if [ -n "${STUDIOLINK_BIN:-}" ]; then bin=$STUDIOLINK_BIN
elif [ "$(id -u)" = 0 ]; then bin=/usr/local/bin
else bin=$HOME/.local/bin
fi
mkdir -p "$bin"
url="$base/download/studiolink-client-$os-$arch"
tmp="$bin/.studiolink-client.$$"
if command -v curl >/dev/null 2>&1; then curl -fsSL "$url" -o "$tmp"
elif command -v wget >/dev/null 2>&1; then wget -qO "$tmp" "$url"
else echo "studiolink: needs curl or wget" >&2; exit 1
fi
chmod +x "$tmp"
mv -f "$tmp" "$bin/studiolink-client"
echo "Installed $bin/studiolink-client ($("$bin/studiolink-client" version))"
case ":$PATH:" in
  *":$bin:"*) ;;
  *) echo "Add $bin to PATH, for example: export PATH=\"$bin:\$PATH\"" ;;
esac
echo "Next: studiolink-client check"
`, "BASE", base)
}
