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
	if len(c.Listen) == 0 {
		return errors.New("listen must name at least one host:port")
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
