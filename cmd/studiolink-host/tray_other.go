//go:build !windows

package main

import (
	"fmt"
	"io"
)

func runTray(_ []string, _, stderr io.Writer) int {
	fmt.Fprintln(stderr, "tray is only available on Windows; use serve")
	return 2
}

func runStart(_ []string, _, stderr io.Writer) int {
	fmt.Fprintln(stderr, "start is only available on Windows; use serve")
	return 2
}

func runAutostart(_ []string, _, stderr io.Writer) int {
	fmt.Fprintln(stderr, "autostart is only available on Windows")
	return 2
}
