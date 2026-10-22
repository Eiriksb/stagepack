//go:build !windows && !linux

package ui

import "os"

// NewTray is the headless UI on stdout: the tray is built for Windows and Linux only.
func NewTray(a Actions) UI { return NewHeadless(os.Stdout, a) }
