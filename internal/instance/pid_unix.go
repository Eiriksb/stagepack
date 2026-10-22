//go:build !windows

package instance

import (
	"errors"
	"syscall"
)

// pidRunning uses signal 0: it delivers nothing but reports whether the process exists.
// EPERM means it exists but belongs to someone else.
func pidRunning(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
