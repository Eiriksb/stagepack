//go:build !windows

package install

import "os"

// The file type is registered the freedesktop.org way (xdg.go).
var platformRegistry assoc = &xdgDesktop{}

// scheduleSelfDelete deletes the running program, and then home if it is empty,
// right away: unlike Windows, a running program's file can be removed.
func scheduleSelfDelete(self, home string) {
	os.Remove(self)
	os.Remove(home)
}
