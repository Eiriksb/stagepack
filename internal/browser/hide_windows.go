package browser

import (
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// hide starts the child without a console window.
func hide(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

// terminate ends p. Windows has no polite signal for another program's process;
// --hide-crash-restore-bubble keeps the next start quiet about it.
func terminate(p *os.Process) { p.Kill() }

func ownGroup(*exec.Cmd) {}

// groupGone waits a moment: the browser's helper processes end soon after it.
func groupGone(pid int, max time.Duration) { time.Sleep(500 * time.Millisecond) }
