package proc

import (
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type jobHandle struct {
	mu sync.Mutex
	h  windows.Handle
}

var procNtResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

// start creates the child suspended, puts it in a new kill-on-close Job
// Object, then resumes it. Starting suspended means the child cannot spawn
// anything before it is inside the job.
func start(p *Process, spec Spec) error {
	job, err := newKillOnCloseJob()
	if err != nil {
		return err
	}
	p.job.h = job
	p.cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine:       spec.CmdLine,
		HideWindow:    true,
		CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW,
	}
	if err := p.cmd.Start(); err != nil {
		p.release()
		return err
	}
	if err := adoptAndResume(job, uint32(p.cmd.Process.Pid)); err != nil {
		_ = p.cmd.Process.Kill()
		p.release()
		return err
	}
	return nil
}

func newKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("create job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return 0, fmt.Errorf("configure job object: %w", err)
	}
	return job, nil
}

func adoptAndResume(job windows.Handle, pid uint32) error {
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME, false, pid)
	if err != nil {
		return fmt.Errorf("open child process: %w", err)
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		return fmt.Errorf("assign child to job object: %w", err)
	}
	if status, _, _ := procNtResumeProcess.Call(uintptr(h)); status != 0 {
		return fmt.Errorf("resume child: NTSTATUS 0x%x", status)
	}
	return nil
}

func kill(p *Process) error {
	p.job.mu.Lock()
	defer p.job.mu.Unlock()
	if p.job.h == 0 {
		return nil // already exited and released
	}
	if err := windows.TerminateJobObject(p.job.h, 1); err != nil {
		return fmt.Errorf("terminate job object: %w", err)
	}
	return nil
}

// release closes the job handle. With kill-on-close set this also ends any
// process the child left behind.
func (p *Process) release() {
	p.job.mu.Lock()
	defer p.job.mu.Unlock()
	if p.job.h != 0 {
		windows.CloseHandle(p.job.h)
		p.job.h = 0
	}
}
