//go:build !windows

package instance

import (
	"errors"
	"syscall"
)

// PIDRunning reports whether a process with this PID runs. It uses signal 0, which
// delivers nothing but reports whether the process exists; EPERM means it exists but
// belongs to someone else.
func PIDRunning(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
