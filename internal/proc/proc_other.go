//go:build !windows

package proc

import (
	"os"
	"syscall"
)

// jobHandle is unused off Windows; the child gets its own process group
// instead, and Kill signals the whole group.
type jobHandle struct{}

func start(p *Process, _ Spec) error {
	p.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return p.cmd.Start()
}

func kill(p *Process) error {
	err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	if err != nil {
		return os.NewSyscallError("kill", err)
	}
	return nil
}

func (p *Process) release() {}
