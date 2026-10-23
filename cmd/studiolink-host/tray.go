package main

import (
	"fmt"
	"image/color"
	"os"
	"strings"
)

// trayView is what the tray shows for a status.
type trayView struct {
	icon    color.RGBA
	tooltip string   // short; Windows cuts tooltips at 127 characters
	summary string   // the first menu line
	relays  []string // one menu line per relay
}

func viewOf(st *Status) trayView {
	v := trayView{icon: iconIdle}
	switch n := len(st.Sessions); n {
	case 0:
		v.summary = "Waiting for agents"
	case 1:
		v.summary = "1 agent session"
		v.icon = iconActive
	default:
		v.summary = fmt.Sprintf("%d agent sessions", n)
		v.icon = iconActive
	}
	down := 0
	for _, r := range st.Relays {
		host := relayHost(r.URL)
		if r.Connected {
			v.relays = append(v.relays, "Relay "+host+": connected")
		} else {
			down++
			v.relays = append(v.relays, "Relay "+host+": down")
		}
	}
	v.tooltip = "StudioLink: " + strings.ToLower(v.summary[:1]) + v.summary[1:]
	if down > 0 {
		v.icon = iconWarn
		v.tooltip += fmt.Sprintf(", %d relay(s) down", down)
	}
	return v
}

// relayHost shortens wss://vps.example.com:44771/tunnel to vps.example.com.
func relayHost(u string) string {
	if _, rest, ok := strings.Cut(u, "://"); ok {
		u = rest
	}
	if i := strings.IndexAny(u, ":/"); i >= 0 {
		u = u[:i]
	}
	return u
}

// rotateLog keeps the previous run's log as path.1 and starts a new one.
func rotateLog(path string) (*os.File, error) {
	_ = os.Remove(path + ".1")
	_ = os.Rename(path, path+".1")
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
}
