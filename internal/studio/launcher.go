// Package studio finds and starts Roblox's stdio-only Studio MCP server.
package studio

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"studiolink/internal/proc"
)

// LauncherName is the script Roblox installs under %LOCALAPPDATA%\Roblox.
// Always launch through it: StudioMCP.exe lives under a version-specific
// folder that changes with every Studio update.
const LauncherName = "mcp.bat"

// ErrNoLocalAppData means the LOCALAPPDATA environment variable is unset,
// which happens off Windows or in a stripped-down environment.
var ErrNoLocalAppData = errors.New("LOCALAPPDATA is not set")

// LauncherPath returns the expected path of mcp.bat. getenv is usually
// os.Getenv; tests pass a fake.
func LauncherPath(getenv func(string) string) (string, error) {
	dir := getenv("LOCALAPPDATA")
	if dir == "" {
		return "", ErrNoLocalAppData
	}
	return filepath.Join(dir, "Roblox", LauncherName), nil
}

// Spec returns how to start Studio MCP in stdio mode through launcher (the
// path of mcp.bat). Batch files run under cmd.exe; /s strips the outer
// quotes so a launcher path with spaces survives.
func Spec(launcher string) proc.Spec {
	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = `C:\Windows\System32\cmd.exe`
	}
	return proc.Spec{
		Path:    comspec,
		CmdLine: fmt.Sprintf(`cmd.exe /d /s /c ""%s" --stdio"`, launcher),
		Dir:     filepath.Dir(launcher),
	}
}

// FindLauncher returns the path of mcp.bat and checks that it exists.
func FindLauncher() (string, error) {
	path, err := LauncherPath(os.Getenv)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("studio MCP launcher not found at %s (install or update Roblox Studio): %w", path, err)
	}
	return path, nil
}
