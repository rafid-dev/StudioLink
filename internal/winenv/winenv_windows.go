package winenv

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
	user32                    = windows.NewLazySystemDLL("user32.dll")
	procSendMessageTimeoutW   = user32.NewProc("SendMessageTimeoutW")
)

// EnvKey is where the user's own PATH lives, under HKEY_CURRENT_USER.
// Tests point it elsewhere.
var EnvKey = "Environment"

// OpenedFromExplorer reports whether this process owns its console alone,
// which is what double-clicking the exe gives: a shell would share it.
// Tests replace it, so they never act on a real console.
var OpenedFromExplorer = func() bool {
	var pids [2]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), 2)
	return n == 1
}

// AddUserPath appends dir to the user's PATH in the registry, keeping it an
// expandable string, and tells running programs (Explorer, so new
// terminals) that the environment changed. It reports whether dir was added.
func AddUserPath(dir string) (bool, error) {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, EnvKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return false, err
	}
	defer k.Close()
	cur, _, err := k.GetStringValue("Path")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return false, err
	}
	next, added := AddToPath(cur, dir)
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
