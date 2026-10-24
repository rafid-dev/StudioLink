package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"studiolink/internal/autostart"
	"studiolink/internal/config"
	"studiolink/internal/netif"
	"studiolink/internal/studio"
	"studiolink/internal/tokenfile"
)

var (
	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
	user32                    = windows.NewLazySystemDLL("user32.dll")
	procSendMessageTimeoutW   = user32.NewProc("SendMessageTimeoutW")
)

// envKey is where the user's own PATH lives, under HKEY_CURRENT_USER.
// Tests point it elsewhere.
var envKey = "Environment"

// openedFromExplorer reports whether this process owns its console alone,
// which is what double-clicking the exe gives: a shell would share it.
// Tests replace it, so they never run setup for real.
var openedFromExplorer = func() bool {
	var pids [2]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), 2)
	return n == 1
}

// runDoubleClick runs setup and keeps the console open to read.
func runDoubleClick(stdout, stderr io.Writer) int {
	in := bufio.NewReader(os.Stdin)
	code := setup(setupOptions{}, in, stdout, stderr)
	fmt.Fprint(stdout, "\nPress Enter to close.")
	_, _ = in.ReadString('\n')
	return code
}

type setupOptions struct {
	cfgPath, dir                                 string
	yes, noInstall, noPath, noAutostart, noStart bool
}

func runSetup(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fset, path := newFlags("setup", stderr)
	var o setupOptions
	fset.StringVar(&o.dir, "dir", "", `install folder (default %LOCALAPPDATA%\Programs\StudioLink)`)
	fset.BoolVar(&o.yes, "yes", false, "accept every default without asking")
	fset.BoolVar(&o.noInstall, "no-install", false, "run from where this exe is instead of installing it")
	fset.BoolVar(&o.noPath, "no-path", false, "don't add the install folder to your PATH")
	fset.BoolVar(&o.noAutostart, "no-autostart", false, "don't offer to start at login")
	fset.BoolVar(&o.noStart, "no-start", false, "don't start the bridge")
	if fset.Parse(args) != nil {
		return 2
	}
	o.cfgPath = *path
	return setup(o, bufio.NewReader(stdin), stdout, stderr)
}

func setup(o setupOptions, in *bufio.Reader, stdout, stderr io.Writer) int {
	p := &prompter{in: in, out: stdout, yes: o.yes}
	if o.cfgPath == "" {
		def, err := config.DefaultPath()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		o.cfgPath = def
	}
	abs, err := filepath.Abs(o.cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if o.dir == "" {
		o.dir = filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "StudioLink")
	}
	fmt.Fprintln(stdout, "StudioLink setup")
	fmt.Fprintln(stdout)

	// Studio first: without its MCP server there is nothing to share.
	if launcher, err := studio.FindLauncher(); err != nil {
		fmt.Fprintln(stdout, "!  Roblox Studio's MCP server was not found. Install or update Roblox Studio;")
		fmt.Fprintln(stdout, "   the bridge works once "+studio.LauncherName+" exists. Setup continues.")
	} else {
		fmt.Fprintln(stdout, "ok Roblox Studio MCP: "+launcher)
	}

	// The config. An existing one keeps its token, so agents stay connected.
	cfg, err := loadConfig(abs)
	switch {
	case err == nil:
		fmt.Fprintln(stdout, "ok Config: "+abs+" (kept)")
	case !fileExists(abs):
		if cfg, err = config.Default(); err == nil {
			err = cfg.Save(abs)
		}
		if err != nil {
			fmt.Fprintln(stderr, "config:", err)
			return 1
		}
		fmt.Fprintln(stdout, "ok Config: "+abs+" (new, with a new token)")
	default:
		fmt.Fprintln(stderr, err)
		fmt.Fprintln(stderr, "Fix the config, or delete it to start over, and run setup again.")
		return 1
	}
	changed, pathAdded := false, false

	exe := executable()
	if !o.noInstall {
		target := filepath.Join(o.dir, filepath.Base(exe))
		if sameFile(exe, target) {
			fmt.Fprintln(stdout, "ok Installed in "+o.dir)
		} else {
			// The running bridge may hold the old exe open.
			if err := stopBridge(context.Background(), abs, cfg.Token); err == nil {
				changed = true
			}
			if exe, err = installFiles(exe, o.dir); err != nil {
				fmt.Fprintln(stderr, "install:", err)
				return 1
			}
			fmt.Fprintln(stdout, "ok Installed in "+o.dir)
		}
	}

	if !o.noPath {
		dir := filepath.Dir(exe)
		switch added, err := addUserPath(dir); {
		case err != nil:
			fmt.Fprintln(stdout, "!  Could not add "+dir+" to your PATH: "+err.Error())
		case added:
			pathAdded = true
			fmt.Fprintln(stdout, "ok Added to your PATH: open a new terminal to use studiolink-host")
		default:
			fmt.Fprintln(stdout, "ok On your PATH")
		}
	}
	if err := tokenfile.Write(tokenfile.PathFor(abs), cfg.Token); err != nil {
		fmt.Fprintln(stdout, "!  Could not write the token file: "+err.Error())
	} else {
		fmt.Fprintln(stdout, "ok Token file for containers: "+tokenfile.PathFor(abs))
	}
	fmt.Fprintln(stdout)

	// WSL2 in its default NAT mode reaches the PC on its own adapter only.
	adapters, _ := netif.List()
	if a, ok := wslAdapter(cfg, adapters); ok && !wslMirrored() {
		if p.ask("WSL2 is installed. Let its distros use Studio?", true) {
			port := loopbackPort(cfg)
			if port == 0 {
				port = config.DefaultPort
			}
			cfg.ListenInterfaces = append(cfg.ListenInterfaces, config.InterfaceListen{Name: a.Name, Port: port})
			if err := cfg.Save(abs); err != nil {
				fmt.Fprintln(stderr, "config:", err)
				return 1
			}
			changed = true
		}
	}

	if !o.noAutostart {
		cmdLine := autostart.Command(exe, abs)
		if current, _ := autostart.Default.Get(); current == cmdLine {
			fmt.Fprintln(stdout, "ok Starts when you log in")
		} else if p.ask("Start StudioLink when you log in to Windows?", true) {
			if err := autostart.Default.Set(cmdLine); err != nil {
				fmt.Fprintln(stdout, "!  Could not turn on Start at login: "+err.Error())
			}
		}
	}

	if !o.noStart {
		info, err := readRunFile(runFilePath(abs))
		running := err == nil && info.alive()
		switch {
		case running && changed:
			if code := restartIfRunning(abs, cfg.Token, stdout, stderr); code != 0 {
				return code
			}
		case running:
			fmt.Fprintf(stdout, "StudioLink is already running (pid %d).\n", info.PID)
		default:
			if code := detachTray(exe, abs, false, stdout, stderr); code != 0 {
				return code
			}
		}
	}

	// Interactive setup ends on the settings page, where the network,
	// port and Tailscale are chosen.
	if info, err := readRunFile(runFilePath(abs)); !o.yes && err == nil && info.alive() && info.SettingsURL != "" {
		if err := openSettings(abs, cfg.Token); err != nil {
			fmt.Fprintln(stdout, "!  Could not open settings: "+err.Error())
		} else {
			fmt.Fprintln(stdout, "Opened StudioLink settings in your browser: choose where agents can connect there.")
		}
	}

	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Done. Look for the StudioLink icon in the notification area.")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Next, print the commands for where your agent runs:")
	for _, s := range scenarios {
		fmt.Fprintf(stdout, "  studiolink-host connect %-10s %s\n", s.name, s.what)
	}
	if pathAdded {
		fmt.Fprintln(stdout, "(in a new terminal, so studiolink-host is on your PATH)")
	}
	return 0
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// addUserPath appends dir to the user's PATH in the registry, keeping it an
// expandable string, and tells running programs (Explorer, so new
// terminals) that the environment changed.
func addUserPath(dir string) (bool, error) {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, envKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return false, err
	}
	defer k.Close()
	cur, _, err := k.GetStringValue("Path")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return false, err
	}
	next, added := addToPath(cur, dir)
	if !added {
		return false, nil
	}
	if err := k.SetExpandStringValue("Path", next); err != nil {
		return false, err
	}
	broadcastEnvChange()
	return true, nil
}

func broadcastEnvChange() {
	const (
		hwndBroadcast   = 0xffff
		wmSettingChange = 0x001a
		smtoAbortIfHung = 0x0002
	)
	env, _ := windows.UTF16PtrFromString("Environment")
	var result uintptr
	_, _, _ = procSendMessageTimeoutW.Call(hwndBroadcast, wmSettingChange, 0,
		uintptr(unsafe.Pointer(env)), smtoAbortIfHung, 2000, uintptr(unsafe.Pointer(&result)))
}
