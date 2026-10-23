package autostart

import (
	"testing"

	"golang.org/x/sys/windows/registry"
)

func TestCommand(t *testing.T) {
	got := Command(`C:\Program Files\StudioLink\studiolink-host.exe`, `C:\Users\me\AppData\Roaming\StudioLink\studiolink.json`)
	want := `"C:\Program Files\StudioLink\studiolink-host.exe" tray -config "C:\Users\me\AppData\Roaming\StudioLink\studiolink.json"`
	if got != want {
		t.Errorf("Command = %s\nwant      %s", got, want)
	}
}

// TestEntry uses a throwaway HKCU key of its own, never the real Run key.
func TestEntry(t *testing.T) {
	e := Entry{Key: `Software\StudioLinkTest`, Name: "StudioLink"}
	t.Cleanup(func() {
		_ = e.Remove()
		_ = registry.DeleteKey(registry.CURRENT_USER, e.Key)
	})
	if got, err := e.Get(); err != nil || got != "" {
		t.Fatalf("Get before Set = %q, %v", got, err)
	}
	if err := e.Set(`"x.exe" tray`); err != nil {
		t.Fatal(err)
	}
	if got, err := e.Get(); err != nil || got != `"x.exe" tray` {
		t.Errorf("Get = %q, %v", got, err)
	}
	if err := e.Remove(); err != nil {
		t.Fatal(err)
	}
	if err := e.Remove(); err != nil {
		t.Errorf("second Remove: %v", err)
	}
	if got, _ := e.Get(); got != "" {
		t.Errorf("Get after Remove = %q", got)
	}
}
