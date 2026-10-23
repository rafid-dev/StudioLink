package tokenfile

import (
	"os"
	"path/filepath"
	"testing"
)

const token = "tok-0123456789abcdef0123456789abcdef"

func TestWriteThenRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", Name)
	if err := Write(path, token); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil || got != token {
		t.Fatalf("Read = %q, %v", got, err)
	}
	info, _ := os.Stat(path)
	if err := Write(path, token); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.Stat(path); !again.ModTime().Equal(info.ModTime()) {
		t.Error("rewrote an identical file")
	}
}

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		name, data, want string
	}{
		{"bare token", token + "\n", token},
		{"bare token with BOM and CRLF", "\xef\xbb\xbf" + token + "\r\n", token},
		{"env file", "# comment\nOTHER=x\nSTUDIOLINK_TOKEN=" + token + "\n", token},
		{"export and quotes", "export STUDIOLINK_TOKEN=\"" + token + "\"\n", token},
		{"env file without the key", "OTHER=x\n", ""},
		{"empty value", "STUDIOLINK_TOKEN=\n", ""},
		{"empty", "  \n", ""},
	} {
		got, err := Parse([]byte(tc.data))
		if got != tc.want || (tc.want == "") != (err != nil) {
			t.Errorf("%s: Parse = %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}

func TestPathFor(t *testing.T) {
	if got := PathFor(filepath.Join("a", "studiolink.json")); got != filepath.Join("a", Name) {
		t.Errorf("PathFor = %q", got)
	}
}
