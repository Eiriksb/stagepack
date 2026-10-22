package browser

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// hide starts the child without a console window.
func hide(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
