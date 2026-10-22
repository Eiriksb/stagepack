//go:build !windows

package browser

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func hide(*exec.Cmd) {}

// terminate asks p to end: the browser closes its windows and saves its profile.
func terminate(p *os.Process) { p.Signal(syscall.SIGTERM) }

// ownGroup starts the browser in a process group of its own, so that its helper
// processes can be waited for (groupGone).
func ownGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// groupGone waits, at most max, until no process is left in the group of the
// browser whose process was pid: its helpers write to the profile until they end.
func groupGone(pid int, max time.Duration) {
	for deadline := time.Now().Add(max); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if err := syscall.Kill(-pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
	}
}
