//go:build !windows

package winenv

import "errors"

// OpenedFromExplorer is always false off Windows.
var OpenedFromExplorer = func() bool { return false }

// AddUserPath is only supported on Windows.
func AddUserPath(string) (bool, error) { return false, errors.ErrUnsupported }
