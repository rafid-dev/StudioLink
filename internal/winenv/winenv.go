// Package winenv holds the Windows console and PATH helpers that the
// studiolink commands share: detecting a double-click and adding a folder to
// the user's PATH. Off Windows they do nothing.
package winenv

import "strings"

// AddToPath returns the PATH list with dir appended, and whether it was
// missing. Entries compare case-insensitively, ignoring a trailing slash.
func AddToPath(list, dir string) (string, bool) {
	norm := func(s string) string { return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), `\/`)) }
	for entry := range strings.SplitSeq(list, ";") {
		if norm(entry) == norm(dir) {
			return list, false
		}
	}
	if list == "" || strings.HasSuffix(list, ";") {
		return list + dir, true
	}
	return list + ";" + dir, true
}
