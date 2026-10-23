package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultIsValid(t *testing.T) {
	c, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
	other, _ := Default()
	if c.Token == other.Token {
		t.Error("two default configs share a token")
	}
}

func TestValidate(t *testing.T) {
	token := strings.Repeat("x", minTokenLen)
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{name: "loopback", cfg: Config{Token: token, Listen: []string{"127.0.0.1:1"}}},
		{name: "localhost", cfg: Config{Token: token, Listen: []string{"localhost:1"}}},
		{name: "ipv6 loopback", cfg: Config{Token: token, Listen: []string{"[::1]:1"}}},
		{name: "named interface", cfg: Config{Token: token, Listen: []string{"100.64.0.7:1"}}},
		{name: "wildcard allowed", cfg: Config{Token: token, Listen: []string{"0.0.0.0:1"}, AllowAllInterfaces: true}},
		{name: "short token", cfg: Config{Token: "abc", Listen: []string{"127.0.0.1:1"}}, wantErr: "token"},
		{name: "no listen", cfg: Config{Token: token}, wantErr: "at least one"},
		{name: "wildcard v4", cfg: Config{Token: token, Listen: []string{"0.0.0.0:1"}}, wantErr: "every network interface"},
		{name: "wildcard v6", cfg: Config{Token: token, Listen: []string{"[::]:1"}}, wantErr: "every network interface"},
		{name: "empty host", cfg: Config{Token: token, Listen: []string{":1"}}, wantErr: "IP address"},
		{name: "hostname", cfg: Config{Token: token, Listen: []string{"example.com:1"}}, wantErr: "IP address"},
		{name: "port zero", cfg: Config{Token: token, Listen: []string{"127.0.0.1:0"}}, wantErr: "fixed port"},
		{name: "no port", cfg: Config{Token: token, Listen: []string{"127.0.0.1"}}, wantErr: "missing port"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("err = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", FileName)
	c, _ := Default()
	c.AllowedOrigins = []string{"https://example.test"}
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != c.Token || got.Listen[0] != c.Listen[0] || got.AllowedOrigins[0] != c.AllowedOrigins[0] {
		t.Errorf("round trip mismatch: got %+v, want %+v", got, c)
	}
}
