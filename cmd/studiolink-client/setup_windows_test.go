package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"studiolink/internal/tokenfile"
)

// TestSetupInstalls copies the exe into -dir and puts that folder on PATH,
// with the PATH change stubbed out.
func TestSetupInstalls(t *testing.T) {
	var calls []string
	exe := stubSetup(t, &calls)
	if err := os.WriteFile(exe, []byte("exe"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "StudioLink")
	var added []string
	addUserPath = func(d string) (bool, error) {
		added = append(added, d)
		return true, nil
	}
	getenv := homeEnv(t.TempDir(), nil)

	var stdout, stderr bytes.Buffer
	in := "192.168.106.1\n" + testToken + "\n"
	if code := run([]string{"setup", "-dir", dir, "-agents", "none"}, strings.NewReader(in), &stdout, &stderr, getenv); code != 0 {
		t.Fatalf("setup: %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	installed := filepath.Join(dir, "studiolink-client.exe")
	if data, err := os.ReadFile(installed); err != nil || string(data) != "exe" {
		t.Errorf("installed exe = %q, %v", data, err)
	}
	if len(added) != 1 || added[0] != dir {
		t.Errorf("addUserPath calls = %q, want [%q]", added, dir)
	}
	if !strings.Contains(stdout.String(), "ok "+dir+" is on your PATH") {
		t.Errorf("stdout = %q", stdout.String())
	}
	if data, _ := os.ReadFile(savedEnvPath(getenv)); tokenfile.Values(data)[tokenfile.Key] != testToken {
		t.Errorf("saved file = %q", data)
	}

	// Running from the installed copy again installs nothing new.
	added = nil
	executable = func() (string, error) { return installed, nil }
	stdout.Reset()
	in = "192.168.106.1\n" + testToken + "\n"
	if code := run([]string{"setup", "-dir", dir, "-agents", "none"}, strings.NewReader(in), &stdout, &stderr, getenv); code != 0 {
		t.Fatalf("second setup: %d stderr %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "ok Installed in "+dir) {
		t.Errorf("second stdout = %q", stdout.String())
	}
}
