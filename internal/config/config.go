// Package config loads and saves studiolink-host's settings file.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
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
	// AllowAllInterfaces permits wildcard addresses such as 0.0.0.0:PORT.
	AllowAllInterfaces bool `json:"allow_all_interfaces,omitempty"`
	// AllowedOrigins lists browser origins that may call the server. Requests
	// carrying any other Origin header are refused (DNS rebinding defence).
	AllowedOrigins []string `json:"allowed_origins,omitempty"`
	// MaxSessions caps concurrent MCP sessions (one StudioMCP process each).
	MaxSessions int `json:"max_sessions,omitempty"`
	// IdleTimeoutMinutes closes sessions with no requests for this long.
	IdleTimeoutMinutes int `json:"idle_timeout_minutes,omitempty"`
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
	if len(c.Listen) == 0 && len(c.ListenInterfaces) == 0 && c.Tailscale == nil {
		return errors.New("nothing to serve on: set listen, listen_interfaces or tailscale")
	}
	for _, li := range c.ListenInterfaces {
		if li.Name == "" {
			return errors.New("listen_interfaces entry needs a name")
		}
		if li.Port < 1 || li.Port > 65535 {
			return fmt.Errorf("listen_interfaces %q: port must be 1-65535", li.Name)
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

// IdleTimeout returns IdleTimeoutMinutes as a duration, or the default.
func (c *Config) IdleTimeout() time.Duration {
	if c.IdleTimeoutMinutes == 0 {
		return 30 * time.Minute
	}
	return time.Duration(c.IdleTimeoutMinutes) * time.Minute
}
