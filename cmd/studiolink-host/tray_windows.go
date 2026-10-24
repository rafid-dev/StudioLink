package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows"

	"studiolink/internal/autostart"
	"studiolink/internal/netif"
	"studiolink/internal/studio"
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
		return detachTray(executable(), abs, *verbose, stdout, stderr)
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
	// The settings page lives as long as the tray, across bridge restarts.
	t.settings = &settingsServer{
		cfgPath:      abs,
		restart:      t.restartBridge,
		status:       t.bridgeStatus,
		adapters:     netif.List,
		autostart:    autostart.Default,
		autostartCmd: autostart.Command(executable(), abs),
		open:         t.openTarget,
		launcher:     studio.FindLauncher,
	}
	settingsURL, err := t.settings.start()
	if err != nil {
		messageBox("StudioLink could not start", err.Error())
		return 1
	}
	t.settingsURL = settingsURL
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	go t.runBridges(ctx)
	systray.Run(t.onReady, cancel)
	return t.exitCode
}

// runBridges runs the bridge until ctx ends. A settings change restarts it:
// the current run ends and a new one starts with the saved config.
func (t *tray) runBridges(ctx context.Context) {
	for {
		bctx, cancel := context.WithCancel(ctx)
		t.mu.Lock()
		t.cancelBridge = cancel
		t.mu.Unlock()
		err := runBridge(bctx, t.cfgPath, t.log, t.settingsURL, func(b *bridge) {
			t.mu.Lock()
			defer t.mu.Unlock()
			if bctx.Err() == nil {
				t.bridge.Store(b)
			}
		})
		cancel()
		t.mu.Lock()
		t.cancelBridge = nil
		t.bridge.Store(nil)
		again := t.restarting
		t.restarting = false
		t.mu.Unlock()
		if again && ctx.Err() == nil {
			t.log.Info("restarting the bridge with the saved settings")
			continue
		}
		t.done <- err
		return
	}
}

// restartBridge ends the running bridge so runBridges starts it again. The
// status reads as not running until then.
func (t *tray) restartBridge() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cancelBridge == nil {
		return
	}
	t.restarting = true
	t.bridge.Store(nil)
	t.cancelBridge()
}

// bridgeStatus is the running bridge's status, or nil while none is up.
func (t *tray) bridgeStatus() *Status {
	b := t.bridge.Load()
	if b == nil {
		return nil
	}
	st := b.Status()
	return &st
}

// openSettings opens the settings page in the default browser, signed in.
func (t *tray) openSettings() {
	link, err := t.settings.newLoginURL()
	if err == nil {
		err = openURL(link)
	}
	if err != nil {
		t.log.Error("cannot open settings", "err", err)
	}
}

// openTarget shows the config file in its folder, or any other file in
// Notepad.
func (t *tray) openTarget(target string) {
	if target == t.cfgPath {
		_ = exec.Command("explorer.exe", "/select,", t.cfgPath).Start()
		return
	}
	openFile(target)
}

// openURL opens a URL in the default browser.
func openURL(url string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

// detachTray launches exe's tray as a process with no console.
func detachTray(exe, cfgPath string, verbose bool, stdout, stderr io.Writer) int {
	if info, err := readRunFile(runFilePath(cfgPath)); err == nil && info.alive() {
		fmt.Fprintf(stderr, "studiolink-host is already running with this config (pid %d)\n", info.PID)
		return 1
	}
	if _, err := loadConfig(cfgPath); err != nil {
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
	logPath := filepath.Join(filepath.Dir(cfgPath), "studiolink.log")
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	// Wait until it serves, so a script can use the bridge as soon as this
	// returns.
	deadline := time.After(30 * time.Second)
	for {
		if info, err := readRunFile(runFilePath(cfgPath)); err == nil && info.PID == cmd.Process.Pid && info.alive() {
			break
		}
		select {
		case <-exited:
			fmt.Fprintf(stderr, "StudioLink stopped while starting; see %s\n", logPath)
			return 1
		case <-deadline:
			fmt.Fprintf(stderr, "StudioLink did not start within 30 s; see %s\n", logPath)
			return 1
		case <-time.After(100 * time.Millisecond):
		}
	}
	fmt.Fprintf(stdout, "StudioLink is running in the notification area (pid %d).\nLog: %s\n", cmd.Process.Pid, logPath)
	return 0
}

// runStart makes sure a bridge is running in the background, for scripts:
// it starts the tray unless one is already up, and succeeds either way.
func runStart(args []string, stdout, stderr io.Writer) int {
	fset, path := newFlags("start", stderr)
	if fset.Parse(args) != nil {
		return 2
	}
	abs, err := filepath.Abs(*path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if info, err := readRunFile(runFilePath(abs)); err == nil && info.alive() {
		fmt.Fprintf(stdout, "StudioLink is already running (pid %d).\n", info.PID)
		return 0
	}
	return detachTray(executable(), abs, false, stdout, stderr)
}

type tray struct {
	cfgPath, logPath string
	log              *slog.Logger
	bridge           atomic.Pointer[bridge]
	cancel           context.CancelFunc
	done             chan error
	exitCode         int

	settings    *settingsServer
	settingsURL string

	// mu guards cancelBridge and restarting, and keeps the bridge pointer
	// in step with them.
	mu           sync.Mutex
	cancelBridge context.CancelFunc // ends the running bridge, or nil
	restarting   bool               // set while a settings change restarts it

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
	settingsItem := systray.AddMenuItem("Settings…", "Open StudioLink's settings in your browser")
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
			case <-settingsItem.ClickedCh:
				t.openSettings()
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
