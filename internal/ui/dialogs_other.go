//go:build !windows

package ui

// The install dialogs are Windows only. Elsewhere they decline and show nothing.

// Confirm reports false: there is nobody to ask.
func Confirm(title, msg string) bool { return false }

// Notice shows nothing.
func Notice(title, msg string) {}

// Alert shows nothing.
func Alert(title, msg string) {}

// Busy shows nothing.
func Busy(title, msg string) (done func()) { return func() {} }
