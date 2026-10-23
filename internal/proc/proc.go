// Package proc starts child processes that cannot outlive studiolink-host.
//
// On Windows each child runs in its own Job Object with kill-on-close set, so
// killing the child kills everything it spawned (mcp.bat starts cmd.exe,
// which starts StudioMCP.exe), and a crashed host leaves no orphans.
package proc

import (
	"io"
	"os/exec"
	"sync"
)

// Spec describes the process to start.
type Spec struct {
	// Path is the executable.
	Path string
	// Args are the arguments, not including Path. Ignored on Windows when
	// CmdLine is set.
	Args []string
	// CmdLine, if set, is the raw Windows command line (including the
	// program name), passed to CreateProcess unchanged. cmd.exe needs this
	// because it does not follow the usual quoting rules.
	CmdLine string
	// Dir is the working directory; empty means the current one.
	Dir string
}

// Process is a running child with piped stdio.
type Process struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
	job    jobHandle

	done    chan struct{}
	waitErr error
	once    sync.Once
}

// Start starts the process described by spec.
func Start(spec Spec) (*Process, error) {
	cmd := exec.Command(spec.Path, spec.Args...)
	cmd.Dir = spec.Dir
	p := &Process{cmd: cmd, done: make(chan struct{})}
	var err error
	if p.stdin, err = cmd.StdinPipe(); err != nil {
		return nil, err
	}
	if p.stdout, err = cmd.StdoutPipe(); err != nil {
		return nil, err
	}
	if p.stderr, err = cmd.StderrPipe(); err != nil {
		return nil, err
	}
	if err := start(p, spec); err != nil {
		return nil, err
	}
	go func() {
		p.waitErr = cmd.Wait()
		p.release()
		close(p.done)
	}()
	return p, nil
}

// Stdin is the child's standard input.
func (p *Process) Stdin() io.WriteCloser { return p.stdin }

// Stdout is the child's standard output.
func (p *Process) Stdout() io.Reader { return p.stdout }

// Stderr is the child's standard error.
func (p *Process) Stderr() io.Reader { return p.stderr }

// Pid is the child's process ID.
func (p *Process) Pid() int { return p.cmd.Process.Pid }

// Done is closed once the child has exited.
func (p *Process) Done() <-chan struct{} { return p.done }

// Err returns the child's exit error. Only meaningful after Done is closed.
func (p *Process) Err() error { return p.waitErr }

// Kill stops the child and every process it started. It is safe to call
// more than once and after the child has exited.
func (p *Process) Kill() error {
	var err error
	p.once.Do(func() { err = kill(p) })
	return err
}
