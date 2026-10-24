package winenv

import "testing"

func TestAddToPath(t *testing.T) {
	for _, tc := range []struct {
		name, list, dir, want string
		added                 bool
	}{
		{"empty", "", `C:\Tools`, `C:\Tools`, true},
		{"appends", `C:\Windows`, `C:\Tools`, `C:\Windows;C:\Tools`, true},
		{"trailing separator", `C:\Windows;`, `C:\Tools`, `C:\Windows;C:\Tools`, true},
		{"already there", `C:\Windows;C:\Tools`, `C:\Tools`, `C:\Windows;C:\Tools`, false},
		{"case and trailing slash", `c:\tools\`, `C:\Tools`, `c:\tools\`, false},
		{"prefix is not a match", `C:\Tools2`, `C:\Tools`, `C:\Tools2;C:\Tools`, true},
	} {
		got, added := AddToPath(tc.list, tc.dir)
		if got != tc.want || added != tc.added {
			t.Errorf("%s: AddToPath(%q, %q) = %q, %v; want %q, %v", tc.name, tc.list, tc.dir, got, added, tc.want, tc.added)
		}
	}
}
