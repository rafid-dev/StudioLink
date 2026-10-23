package main

import (
	"testing"

	"golang.org/x/sys/windows/registry"
)

// TestAddUserPath uses a throwaway HKCU key, never the real Environment.
func TestAddUserPath(t *testing.T) {
	old := envKey
	envKey = `Software\StudioLinkTest\Environment`
	t.Cleanup(func() {
		envKey = old
		_ = registry.DeleteKey(registry.CURRENT_USER, `Software\StudioLinkTest\Environment`)
		_ = registry.DeleteKey(registry.CURRENT_USER, `Software\StudioLinkTest`)
	})
	k, _, err := registry.CreateKey(registry.CURRENT_USER, envKey, registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.SetExpandStringValue("Path", `%USERPROFILE%\bin`); err != nil {
		t.Fatal(err)
	}
	k.Close()

	for i, want := range []bool{true, false} {
		added, err := addUserPath(`C:\Tools\StudioLink`)
		if err != nil || added != want {
			t.Fatalf("call %d: addUserPath = %v, %v; want %v", i, added, err, want)
		}
	}
	k, err = registry.OpenKey(registry.CURRENT_USER, envKey, registry.QUERY_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	got, typ, err := k.GetStringValue("Path")
	if err != nil || got != `%USERPROFILE%\bin;C:\Tools\StudioLink` || typ != registry.EXPAND_SZ {
		t.Errorf("Path = %q (type %d), %v", got, typ, err)
	}
}
