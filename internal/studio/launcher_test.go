package studio

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestLauncherPath(t *testing.T) {
	env := map[string]string{"LOCALAPPDATA": filepath.Join("C:", "Users", "dev", "AppData", "Local")}
	got, err := LauncherPath(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("LauncherPath: %v", err)
	}
	want := filepath.Join(env["LOCALAPPDATA"], "Roblox", "mcp.bat")
	if got != want {
		t.Errorf("LauncherPath = %q, want %q", got, want)
	}
}

func TestLauncherPathNoLocalAppData(t *testing.T) {
	_, err := LauncherPath(func(string) string { return "" })
	if !errors.Is(err, ErrNoLocalAppData) {
		t.Errorf("err = %v, want ErrNoLocalAppData", err)
	}
}
