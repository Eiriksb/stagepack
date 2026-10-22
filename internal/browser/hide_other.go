//go:build !windows

package browser

import "os/exec"

func hide(*exec.Cmd) {}
