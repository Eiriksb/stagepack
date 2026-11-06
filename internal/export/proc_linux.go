package export

import (
	"errors"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// procCtl controls the process group the exporter runs in.
type procCtl struct{ pgid int }

// prepare puts the child in its own process group, and has the kernel kill it
// if this process dies.
func prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}

func attach(cmd *exec.Cmd) (*procCtl, error) { return &procCtl{pgid: cmd.Process.Pid}, nil }

// stop sends SIGTERM to the group, then SIGKILL after grace.
func (p *procCtl) stop(exited <-chan struct{}, grace time.Duration) error {
	_ = syscall.Kill(-p.pgid, syscall.SIGTERM)
	select {
	case <-exited:
		return nil
	case <-time.After(grace):
	}
	_ = syscall.Kill(-p.pgid, syscall.SIGKILL)
	select {
	case <-exited:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("the exporter did not exit after SIGKILL")
	}
}

// cleanup kills what is left of the group once its leader is gone.
func (p *procCtl) cleanup() { _ = syscall.Kill(-p.pgid, syscall.SIGKILL) }

// pidRunning uses signal 0, which delivers nothing but tells whether the
// process exists; EPERM means it exists and belongs to someone else.
func pidRunning(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// reveal opens the folder holding path in the file manager, without waiting.
func reveal(path string) error {
	cmd := exec.Command("xdg-open", filepath.Dir(path))
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // reap it; its exit status does not matter
	return nil
}
