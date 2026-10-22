// Package browser opens a URL in the user's default browser.
package browser

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// Open opens url in the default browser and returns without waiting for it:
// on Windows through rundll32 url.dll,FileProtocolHandler (no console window),
// elsewhere through xdg-open. If $BROWSER is set (tests), that program is run
// with url as its only argument instead, without a shell.
func Open(url string) error {
	var cmd *exec.Cmd
	switch b := os.Getenv("BROWSER"); {
	case b != "":
		cmd = exec.Command(b, url)
	case runtime.GOOS == "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	hide(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open the browser: %w", err)
	}
	go cmd.Wait() // reap it; its exit status does not matter
	return nil
}
