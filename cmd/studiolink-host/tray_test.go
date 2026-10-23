package main

import (
	"os"
	"path/filepath"
	"testing"

	"studiolink/internal/hostserver"
)

func TestViewOf(t *testing.T) {
	st := Status{}
	v := viewOf(&st)
	if v.icon != iconIdle || v.summary != "Waiting for agents" || v.tooltip != "StudioLink: waiting for agents" {
		t.Errorf("idle view = %+v", v)
	}

	st.Sessions = make([]hostserver.SessionInfo, 2)
	st.Relays = []RelayStatus{
		{URL: "wss://vps.example.com:44771/tunnel", Connected: true},
		{URL: "wss://other.example.com/tunnel"},
	}
	v = viewOf(&st)
	if v.icon != iconWarn {
		t.Errorf("icon = %v, want warn", v.icon)
	}
	if v.summary != "2 agent sessions" {
		t.Errorf("summary = %q", v.summary)
	}
	if v.tooltip != "StudioLink: 2 agent sessions, 1 relay(s) down" {
		t.Errorf("tooltip = %q", v.tooltip)
	}
	want := []string{"Relay vps.example.com: connected", "Relay other.example.com: down"}
	if len(v.relays) != 2 || v.relays[0] != want[0] || v.relays[1] != want[1] {
		t.Errorf("relays = %q", v.relays)
	}
}

func TestRotateLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "studiolink.log")
	for _, text := range []string{"first", "second", "third"} {
		f, err := rotateLog(path)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(text)
		f.Close()
	}
	if b, _ := os.ReadFile(path); string(b) != "third" {
		t.Errorf("log = %q", b)
	}
	if b, _ := os.ReadFile(path + ".1"); string(b) != "second" {
		t.Errorf("log.1 = %q", b)
	}
}
