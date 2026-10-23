// Package netif finds network adapters by name and keeps listeners bound to
// their current IPv4 addresses.
package netif

import (
	"fmt"
	"net"
	"strings"
)

// Adapter is one network adapter and its usable IPv4 addresses.
type Adapter struct {
	Name string
	Up   bool
	IPv4 []net.IP
}

// Kind guesses what an adapter connects to, for the interfaces listing.
func (a Adapter) Kind() string {
	name := strings.ToLower(a.Name)
	switch {
	case strings.Contains(name, "loopback"):
		return "this PC; Docker Desktop reaches it as host.docker.internal"
	case strings.Contains(name, "(wsl"):
		return "WSL2 distros (NAT mode)"
	case strings.Contains(name, "default switch"):
		return "Hyper-V VMs on the Default Switch"
	case strings.Contains(name, "vmnet1"):
		return "VMware host-only VMs"
	case strings.Contains(name, "vmnet8"):
		return "VMware NAT VMs"
	case strings.Contains(name, "virtualbox") || a.hasPrefix("192.168.56."):
		return "VirtualBox host-only VMs (probably)"
	case strings.Contains(name, "tailscale"):
		return "Tailscale (the tailscale app's adapter)"
	case strings.HasPrefix(name, "vethernet"):
		return "Hyper-V virtual switch"
	default:
		return "physical network: exposes the bridge to your LAN"
	}
}

func (a Adapter) hasPrefix(prefix string) bool {
	for _, ip := range a.IPv4 {
		if strings.HasPrefix(ip.String(), prefix) {
			return true
		}
	}
	return false
}

// List returns every adapter with at least one usable IPv4 address.
func List() ([]Adapter, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []Adapter
	for _, iface := range ifaces {
		ips := usableIPv4(iface)
		if len(ips) == 0 {
			continue
		}
		out = append(out, Adapter{Name: iface.Name, Up: iface.Flags&net.FlagUp != 0, IPv4: ips})
	}
	return out, nil
}

// ErrNotFound means no adapter has the requested name.
type ErrNotFound struct{ Name string }

func (e ErrNotFound) Error() string {
	return fmt.Sprintf("no network adapter named %q (run studiolink-host interfaces)", e.Name)
}

// IPv4 returns the usable IPv4 addresses of the adapter called name
// (case-insensitive). An adapter that is down has none.
func IPv4(name string) ([]net.IP, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for _, iface := range ifaces {
		if strings.EqualFold(iface.Name, name) {
			if iface.Flags&net.FlagUp == 0 {
				return nil, nil
			}
			return usableIPv4(iface), nil
		}
	}
	return nil, ErrNotFound{name}
}

// usableIPv4 skips link-local (169.254.x.x) addresses, which Windows assigns
// to disconnected adapters and nothing can usefully reach.
func usableIPv4(iface net.Interface) []net.IP {
	addrs, err := iface.Addrs()
	if err != nil {
		return nil
	}
	var ips []net.IP
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipnet.IP.To4()
		if ip == nil || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			continue
		}
		ips = append(ips, ip)
	}
	return ips
}
