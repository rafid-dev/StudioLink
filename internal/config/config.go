// Package config loads and saves studiolink-host's settings file.
package config

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"studiolink/internal/policy"
	"studiolink/internal/tunnel"
)

// DefaultPort is studiolink-host's default listen port. It avoids 44755
// (Roblox's open-source Rust MCP server) and the ports the Yeetback and
// Prison Wars bridges use.
const DefaultPort = 44770

// FileName is the settings file name inside the config directory.
const FileName = "studiolink.json"

// minTokenLen rejects tokens short enough to brute-force.
const minTokenLen = 32

// Config is the settings file's contents.
type Config struct {
	// Token is the shared secret every request must present.
	Token string `json:"token"`
	// Listen is the list of host:port addresses to serve on.
	Listen []string `json:"listen"`
	// ListenInterfaces serves on network adapters by name, following their
	// IPv4 addresses as they come and go (WSL and Hyper-V adapters change
	// address on reboot, and the WSL adapter only exists while WSL runs).
	ListenInterfaces []InterfaceListen `json:"listen_interfaces,omitempty"`
	// Tailscale, when set, also serves on the tailnet as its own device.
	Tailscale *Tailscale `json:"tailscale,omitempty"`
	// Relays are servers to dial out to (see Relay).
	Relays []Relay `json:"relays,omitempty"`
	// AllowAllInterfaces permits wildcard addresses such as 0.0.0.0:PORT.
	AllowAllInterfaces bool `json:"allow_all_interfaces,omitempty"`
	// AllowedOrigins lists browser origins that may call the server. Requests
	// carrying any other Origin header are refused (DNS rebinding defence).
	AllowedOrigins []string `json:"allowed_origins,omitempty"`
	// MaxSessions caps concurrent MCP sessions (one StudioMCP process each).
	MaxSessions int `json:"max_sessions,omitempty"`
	// IdleTimeoutMinutes closes sessions with no requests for this long.
	IdleTimeoutMinutes int `json:"idle_timeout_minutes,omitempty"`
	// Tools limits which Studio tools the main token may list and call.
	Tools *policy.Tools `json:"tools,omitempty"`
	// Clients are further tokens, each with a name (for logs and the audit
	// log) and its own tool policy.
	Clients []Client `json:"clients,omitempty"`
	// AuditLog is where to record sessions and tool calls. Defaults to
	// audit.jsonl next to the config; "off" disables it.
	AuditLog string `json:"audit_log,omitempty"`
	// AuditArguments also records each tool call's arguments, which can
	// contain whole scripts.
	AuditArguments bool `json:"audit_arguments,omitempty"`
}

// AuditOff is the AuditLog value that disables the audit log.
const AuditOff = "off"

// Client is an extra token, for example a read-only one for an agent on a
// shared server.
type Client struct {
	Name  string        `json:"name"`
	Token string        `json:"token"`
	Tools *policy.Tools `json:"tools,omitempty"`
}

// InterfaceListen serves on every IPv4 address of one network adapter.
type InterfaceListen struct {
	// Name is the adapter name as `studiolink-host interfaces` prints it
	// (case-insensitive), for example "vEthernet (Default Switch)".
	Name string `json:"name"`
	Port int    `json:"port"`
}

// Tailscale configures the embedded tailnet device (tsnet).
type Tailscale struct {
	// Hostname is the device name on the tailnet. Defaults to "studiolink".
	Hostname string `json:"hostname,omitempty"`
	// Port defaults to 80, or 443 with HTTPS.
	Port int `json:"port,omitempty"`
	// HTTPS serves TLS with the tailnet's certificate. Needs HTTPS
	// certificates enabled in the Tailscale admin console.
	HTTPS bool `json:"https,omitempty"`
	// StateDir holds the device's identity. Defaults to a "tsnet" folder
	// next to the config file.
	StateDir string `json:"state_dir,omitempty"`
	// Ephemeral removes the device from the tailnet when the bridge stops.
	Ephemeral bool `json:"ephemeral,omitempty"`
}

// DeviceName returns Hostname or its default.
func (t *Tailscale) DeviceName() string {
	if t.Hostname == "" {
		return "studiolink"
	}
	return t.Hostname
}

// ListenPort returns Port or its default.
func (t *Tailscale) ListenPort() int {
	switch {
	case t.Port != 0:
		return t.Port
	case t.HTTPS:
		return 443
	default:
		return 80
	}
}

// Relay is a server running `studiolink-client relay` that the host dials
// out to, so agents there reach Studio without this PC accepting
// connections (dial-out mode).
type Relay struct {
	// URL is the relay's tunnel address, for example
	// wss://vps.example.com:44771. The /tunnel path is added if missing.
	URL string `json:"url"`
	// Fingerprint pins the relay's self-signed certificate, as printed by
	// `studiolink-client relay`. Leave it out when the relay has a
	// certificate from a public CA.
	Fingerprint string `json:"fingerprint,omitempty"`
}

// TunnelURL returns URL with the tunnel path filled in.
func (r Relay) TunnelURL() string {
	u, err := url.Parse(r.URL)
	if err != nil {
		return r.URL
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = tunnel.Path
	}
	return u.String()
}

func (r Relay) validate() error {
	u, err := url.Parse(r.URL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("relay url %q: want wss://host:port", r.URL)
	}
	switch u.Scheme {
	case "wss":
	case "ws":
		// Unencrypted only to this machine, e.g. a local reverse proxy.
		if ip := net.ParseIP(u.Hostname()); u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("relay url %q: use wss:// (ws:// is only allowed to localhost)", r.URL)
		}
	default:
		return fmt.Errorf("relay url %q: want wss://host:port", r.URL)
	}
	if r.Fingerprint != "" && !tunnel.ValidFingerprint(r.Fingerprint) {
		return fmt.Errorf("relay %q: fingerprint must look like sha256:<64 hex digits>", r.URL)
	}
	return nil
}

// Default returns a loopback-only config with a fresh token.
func Default() (*Config, error) {
	token, err := NewToken()
	if err != nil {
		return nil, err
	}
	return &Config{
		Token:  token,
		Listen: []string{fmt.Sprintf("127.0.0.1:%d", DefaultPort)},
	}, nil
}

// NewToken returns a random 256-bit token, base64url-encoded.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// DefaultPath returns %APPDATA%\StudioLink\studiolink.json (or the platform
// equivalent).
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "StudioLink", FileName), nil
}

// Load reads and validates the config at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// Notepad and Windows PowerShell 5.1 save UTF-8 with a byte order mark,
	// which encoding/json rejects.
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// Save writes c to path, creating the directory. The file holds the token,
// so it is readable by the current user only where the OS honours modes.
func (c *Config) Save(path string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// Validate checks the settings that keep the bridge safe to expose.
func (c *Config) Validate() error {
	if len(c.Token) < minTokenLen {
		return fmt.Errorf("token must be at least %d characters (run studiolink-host init)", minTokenLen)
	}
	if err := c.Tools.Validate(); err != nil {
		return fmt.Errorf("tools: %w", err)
	}
	names := map[string]bool{}
	tokens := map[string]bool{c.Token: true}
	for _, cl := range c.Clients {
		switch {
		case cl.Name == "" || cl.Name == "owner":
			return fmt.Errorf("clients: each client needs a name other than \"owner\"")
		case names[cl.Name]:
			return fmt.Errorf("clients: name %q is used twice", cl.Name)
		case len(cl.Token) < minTokenLen:
			return fmt.Errorf("client %q: token must be at least %d characters (studiolink-host token -new makes one)", cl.Name, minTokenLen)
		case tokens[cl.Token]:
			return fmt.Errorf("client %q: token is already used by another client or the main token", cl.Name)
		}
		if err := cl.Tools.Validate(); err != nil {
			return fmt.Errorf("client %q tools: %w", cl.Name, err)
		}
		names[cl.Name], tokens[cl.Token] = true, true
	}
	if len(c.Listen) == 0 && len(c.ListenInterfaces) == 0 && c.Tailscale == nil && len(c.Relays) == 0 {
		return errors.New("nothing to serve on: set listen, listen_interfaces, tailscale or relays")
	}
	for _, li := range c.ListenInterfaces {
		if li.Name == "" {
			return errors.New("listen_interfaces entry needs a name")
		}
		if li.Port < 1 || li.Port > 65535 {
			return fmt.Errorf("listen_interfaces %q: port must be 1-65535", li.Name)
		}
	}
	for _, r := range c.Relays {
		if err := r.validate(); err != nil {
			return err
		}
	}
	if t := c.Tailscale; t != nil {
		if t.Port < 0 || t.Port > 65535 {
			return errors.New("tailscale.port must be 1-65535")
		}
	}
	for _, addr := range c.Listen {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return fmt.Errorf("listen address %q: %w", addr, err)
		}
		if port == "" || port == "0" {
			return fmt.Errorf("listen address %q: needs a fixed port", addr)
		}
		if host == "localhost" {
			continue
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return fmt.Errorf("listen address %q: host must be an IP address or localhost", addr)
		}
		if ip.IsUnspecified() && !c.AllowAllInterfaces {
			return fmt.Errorf("listen address %q exposes every network interface; name one interface's IP, or set allow_all_interfaces", addr)
		}
	}
	if c.MaxSessions < 0 || c.IdleTimeoutMinutes < 0 {
		return errors.New("max_sessions and idle_timeout_minutes must not be negative")
	}
	return nil
}

// SessionLimit returns MaxSessions, or the default when unset.
func (c *Config) SessionLimit() int {
	if c.MaxSessions == 0 {
		return 16
	}
	return c.MaxSessions
}

// AuditPath returns where the audit log goes for a config loaded from
// configPath, or "" when it is off.
func (c *Config) AuditPath(configPath string) string {
	switch c.AuditLog {
	case AuditOff:
		return ""
	case "":
		return filepath.Join(filepath.Dir(configPath), "audit.jsonl")
	}
	return c.AuditLog
}

// IdleTimeout returns IdleTimeoutMinutes as a duration, or the default.
func (c *Config) IdleTimeout() time.Duration {
	if c.IdleTimeoutMinutes == 0 {
		return 30 * time.Minute
	}
	return time.Duration(c.IdleTimeoutMinutes) * time.Minute
}
