package slidev

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// procCtl is a job object that kills everything in it when closed.
type procCtl struct {
	job  windows.Handle
	once sync.Once
}

// prepare hides the console window the child would otherwise get.
func prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

// attach puts the started process in a new kill-on-close job. Children it
// starts afterwards join the job too.
func attach(cmd *exec.Cmd) (*procCtl, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create job object: %w", err)
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("set job limits: %w", err)
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("open process: %w", err)
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("assign process to job: %w", err)
	}
	return &procCtl{job: job}, nil
}

func (p *procCtl) closeJob() { p.once.Do(func() { windows.CloseHandle(p.job) }) }

// stop closes the job, which terminates everything in it.
func (p *procCtl) stop(exited <-chan struct{}, grace time.Duration) error {
	p.closeJob()
	select {
	case <-exited:
		return nil
	case <-time.After(grace):
		return errors.New("slidev did not exit after its job was closed")
	}
}

// cleanup closes the job once the leader is gone, ending any leftovers.
func (p *procCtl) cleanup() { p.closeJob() }
