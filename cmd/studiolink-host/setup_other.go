//go:build !windows

package main

import (
	"fmt"
	"io"
)

var openedFromExplorer = func() bool { return false }

func runDoubleClick(_, _ io.Writer) int { return 2 }

func runSetup(_ []string, _ io.Reader, _, stderr io.Writer) int {
	fmt.Fprintln(stderr, "setup is only available on Windows; use init and serve")
	return 2
}
