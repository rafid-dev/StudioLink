package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows"

	"studiolink/internal/autostart"
)

// runTray starts the bridge in the background with an icon in the
// notification area. Without -background it relaunches itself detached
// from the console (so a login item shows no window) and returns.
func runTray(args []string, stdout, stderr io.Writer) int {
	fset, path := newFlags("tray", stderr)
	verbose := fset.Bool("v", false, "log every request and StudioMCP's stderr")
	background := fset.Bool("background", false, "run in this process (used by the relaunch)")
	if fset.Parse(args) != nil {
		return 2
	}
	abs, err := filepath.Abs(*path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !*background {
		return detachTray(abs, *verbose, stdout, stderr)
	}

	logPath := filepath.Join(filepath.Dir(abs), "studiolink.log")
	logFile, err := rotateLog(logPath)
	if err != nil {
		messageBox("StudioLink could not start", fmt.Sprintf("Cannot write the log %s: %v", logPath, err))
		return 1
	}
	defer logFile.Close()
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: level}))

	t := &tray{cfgPath: abs, logPath: logPath, log: log, done: make(chan error, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	go func() {
		t.done <- runBridge(ctx, abs, log, func(b *bridge) { t.bridge.Store(b) })
	}()
	systray.Run(t.onReady, cancel)
	return t.exitCode
}

// detachTray relaunches the tray as a process with no console.
func detachTray(cfgPath string, verbose bool, stdout, stderr io.Writer) int {
	if info, err := readRunFile(runFilePath(cfgPath)); err == nil && info.alive() {
		fmt.Fprintf(stderr, "studiolink-host is already running with this config (pid %d)\n", info.PID)
		return 1
	}
	if _, err := loadConfig(cfgPath); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	args := []string{"tray", "-background", "-config", cfgPath}
	if verbose {
		args = append(args, "-v")
	}
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(stderr, "cannot start the tray:", err)
		return 1
	}
	_ = cmd.Process.Release()
	fmt.Fprintf(stdout, "StudioLink is running in the notification area (pid %d).\nLog: %s\n",
		cmd.Process.Pid, filepath.Join(filepath.Dir(cfgPath), "studiolink.log"))
	return 0
}

type tray struct {
	cfgPath, logPath string
	log              *slog.Logger
	bridge           atomic.Pointer[bridge]
	cancel           context.CancelFunc
	done             chan error
	exitCode         int

	summary *systray.MenuItem
	relays  []*systray.MenuItem
	login   *systray.MenuItem
	icon    [4]byte // the RGBA last drawn, to skip redundant SetIcon calls
}

func (t *tray) onReady() {
	systray.SetIcon(trayIcon(iconIdle))
	systray.SetTitle("StudioLink")
	systray.SetTooltip("StudioLink: starting")

	t.summary = systray.AddMenuItem("Starting...", "")
	t.summary.Disable()
	if cfg, err := loadConfig(t.cfgPath); err == nil {
		for range cfg.Relays {
			item := systray.AddMenuItem("", "")
			item.Disable()
			item.Hide()
			t.relays = append(t.relays, item)
		}
	}
	systray.AddSeparator()
	openLog := systray.AddMenuItem("Open log", t.logPath)
	openAudit := systray.AddMenuItem("Open audit log", "")
	openFolder := systray.AddMenuItem("Open config folder", t.cfgPath)
	cmdLine := autostart.Command(executable(), t.cfgPath)
	current, _ := autostart.Default.Get()
	t.login = systray.AddMenuItemCheckbox("Start at login", "Run this tray when you log in to Windows", current == cmdLine)
	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit StudioLink", "Stop the bridge and end every agent session")

	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				t.refresh()
			case <-openLog.ClickedCh:
				openFile(t.logPath)
			case <-openAudit.ClickedCh:
				if b := t.bridge.Load(); b != nil && b.audit.Path() != "" {
					openFile(b.audit.Path())
				}
			case <-openFolder.ClickedCh:
				_ = exec.Command("explorer.exe", "/select,", t.cfgPath).Start()
			case <-t.login.ClickedCh:
				t.toggleLogin(cmdLine)
			case <-quit.ClickedCh:
				t.cancel()
				t.finish(<-t.done)
				return
			case err := <-t.done:
				t.finish(err)
				return
			}
		}
	}()
}

func (t *tray) refresh() {
	b := t.bridge.Load()
	if b == nil {
		return
	}
	st := b.Status()
	v := viewOf(&st)
	t.summary.SetTitle(v.summary)
	for i, item := range t.relays {
		if i < len(v.relays) {
			item.SetTitle(v.relays[i])
			item.Show()
		}
	}
	systray.SetTooltip(v.tooltip)
	if key := [4]byte{v.icon.R, v.icon.G, v.icon.B, v.icon.A}; key != t.icon {
		t.icon = key
		systray.SetIcon(trayIcon(v.icon))
	}
}

func (t *tray) toggleLogin(cmdLine string) {
	var err error
	if t.login.Checked() {
		err = autostart.Default.Remove()
	} else {
		err = autostart.Default.Set(cmdLine)
	}
	if err != nil {
		t.log.Error("cannot change Start at login", "err", err)
		messageBox("StudioLink", "Cannot change Start at login: "+err.Error())
		return
	}
	if t.login.Checked() {
		t.login.Uncheck()
		t.log.Info("start at login turned off")
	} else {
		t.login.Check()
		t.log.Info("start at login turned on", "command", cmdLine)
	}
}

// finish ends the tray once the bridge has stopped; err says why if it
// stopped on its own.
func (t *tray) finish(err error) {
	if err != nil {
		t.exitCode = 1
		messageBox("StudioLink stopped", err.Error()+"\n\nLog: "+t.logPath)
	}
	systray.Quit()
}

func executable() string {
	exe, err := os.Executable()
	if err != nil {
		return "studiolink-host.exe"
	}
	return exe
}

// openFile shows a text file in Notepad, which every Windows install has
// and which opens .jsonl without asking for an app.
func openFile(path string) {
	_ = exec.Command("notepad.exe", path).Start()
}

func messageBox(title, text string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	_, _ = windows.MessageBox(0, m, t, windows.MB_OK|windows.MB_ICONERROR|windows.MB_SETFOREGROUND)
}

// runAutostart shows or changes whether the tray starts at login.
func runAutostart(args []string, stdout, stderr io.Writer) int {
	fset, path := newFlags("autostart", stderr)
	fset.Usage = func() {
		fmt.Fprintln(stderr, "usage: studiolink-host autostart [-config PATH] [on|off]")
		fset.PrintDefaults()
	}
	if fset.Parse(args) != nil {
		return 2
	}
	abs, err := filepath.Abs(*path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	cmdLine := autostart.Command(executable(), abs)
	switch fset.Arg(0) {
	case "":
	case "on":
		if _, err := loadConfig(abs); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := autostart.Default.Set(cmdLine); err != nil {
			fmt.Fprintln(stderr, "autostart:", err)
			return 1
		}
	case "off":
		if err := autostart.Default.Remove(); err != nil {
			fmt.Fprintln(stderr, "autostart:", err)
			return 1
		}
	default:
		fset.Usage()
		return 2
	}
	current, err := autostart.Default.Get()
	if err != nil {
		fmt.Fprintln(stderr, "autostart:", err)
		return 1
	}
	switch current {
	case "":
		fmt.Fprintln(stdout, "Start at login: off")
	case cmdLine:
		fmt.Fprintln(stdout, "Start at login: on\n  "+current)
	default:
		fmt.Fprintln(stdout, "Start at login: on, with a different binary or config:\n  "+current+"\nRun \"studiolink-host autostart on\" to use this one.")
	}
	return 0
}
