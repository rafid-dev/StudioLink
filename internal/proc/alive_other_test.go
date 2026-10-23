//go:build !windows

package proc

import "syscall"

func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
