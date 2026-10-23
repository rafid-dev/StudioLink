// Package autostart starts studiolink-host's tray when the user logs in,
// through the per-user Run registry key. It needs no administrator rights
// and runs the bridge in the user's own session, next to Studio.
package autostart

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// Entry is one program started at login.
type Entry struct {
	Key  string // under HKEY_CURRENT_USER
	Name string // the value name
}

// Default is StudioLink's entry in the standard Run key.
var Default = Entry{Key: `Software\Microsoft\Windows\CurrentVersion\Run`, Name: "StudioLink"}

// Command builds the command line that starts the tray for a config.
func Command(exe, configPath string) string {
	return fmt.Sprintf(`%s tray -config %s`, quote(exe), quote(configPath))
}

func quote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// Get returns the registered command, or "" when there is none.
func (e Entry) Get() (string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, e.Key, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer k.Close()
	v, _, err := k.GetStringValue(e.Name)
	if errors.Is(err, registry.ErrNotExist) {
		return "", nil
	}
	return v, err
}

// Set registers command to run at login.
func (e Entry) Set(command string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, e.Key, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(e.Name, command)
}

// Remove stops running anything at login. It is not an error if nothing
// was registered.
func (e Entry) Remove() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, e.Key, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(e.Name); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}
