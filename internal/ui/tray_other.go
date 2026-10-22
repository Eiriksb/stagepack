//go:build !windows

package ui

import "os"

// NewTray is the headless UI on stdout: the tray is Windows only.
func NewTray(a Actions) UI { return NewHeadless(os.Stdout, a) }
