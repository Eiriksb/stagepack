//go:build !windows

package install

// There is no registry here: Install and Uninstall unpack and remove files
// only (this is what the tests run).
type noRegistry struct{}

func (noRegistry) write([]RegValue) error { return nil }
func (noRegistry) remove() error          { return nil }
func (noRegistry) notify()                {}

var platformRegistry assoc = noRegistry{}

func scheduleSelfDelete(self, home string) {}
