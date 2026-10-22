package ui

import (
	"bytes"
	"errors"
	"log"
	"os/exec"
	"strings"

	"github.com/godbus/dbus/v5"

	"stagepack/internal/brand"
)

// trayIcon is trayPNG: the Linux tray takes a PNG.
func trayIcon() []byte { return trayPNG() }

// trayAreaPresent says whether the desktop shows tray icons: whether something
// on the session bus takes StatusNotifierItems (KDE, or GNOME with the
// AppIndicator extension, which Ubuntu turns on). Without it the icon would be
// registered nowhere, and nothing could stop the talk.
var trayAreaPresent = func() bool {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		log.Printf("no session bus, so no tray icon: %v", err)
		return false
	}
	defer conn.Close()
	var has bool
	err = conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").Store(&has)
	if err != nil {
		log.Printf("ask the session bus for a tray: %v", err)
	}
	return err == nil && has
}

// runFallback runs a small window in place of the tray when the desktop shows
// no tray icons, and reports whether it did; it returns once Quit is called.
// The window offers the menu's actions; closing it stops the talk.
func (t *tray) runFallback(ready func()) bool {
	if trayAreaPresent() {
		return false
	}
	log.Print("this desktop shows no tray icons (on GNOME, turn on the AppIndicator extension): a window stands in for the tray")
	quit := make(chan struct{})
	var closeWindow func() // closes the window that is up, if any
	t.mu.Lock()
	t.closeFallback = func() {
		close(quit)
		t.mu.Lock()
		c := closeWindow
		t.mu.Unlock()
		if c != nil {
			c()
		}
	}
	t.mu.Unlock()
	quitting := func() bool {
		select {
		case <-quit:
			return true
		default:
			return false
		}
	}
	run := func(fn func()) {
		if fn != nil {
			fn()
		}
	}
	ready()
	for !quitting() {
		t.mu.Lock()
		status := t.text
		t.mu.Unlock()
		choice, ok := t.controlWindow(status, func(c func()) {
			t.mu.Lock()
			closeWindow = c
			t.mu.Unlock()
			if c != nil && quitting() { // Quit came while the window was opening
				c()
			}
		})
		if quitting() {
			break
		}
		switch {
		case !ok:
			// Nothing can show the window: wait until the talk is stopped some other way.
			notifySend(brand.Name, "The talk is shown, but this desktop has no tray icons and no zenity to stop it with. Install zenity, or turn on tray icons.")
			<-quit
		case choice == menuPresenter:
			go run(t.a.OpenPresenter)
		case choice == menuAudience:
			go run(t.a.OpenAudience)
		case choice == menuSaveCopy:
			run(t.a.SaveCopy) // it asks where first; the window comes back after
		default: // Stop, or the window was closed
			if t.a.Stop == nil {
				return true
			}
			go t.a.Stop() // it ends in Quit
			<-quit
		}
	}
	return true
}

// controlWindow shows the window once and returns the button chosen ("" for
// Stop or a closed window); ok is false if no dialog program could show it.
// started is given a function that closes the window.
func (t *tray) controlWindow(status string, started func(stop func())) (choice string, ok bool) {
	if status == "" {
		status = brand.Name
	}
	msg := status + "\n\nThis window stands in for the tray icon, which this desktop does not show.\n" +
		"Stop, or closing the window, saves the notes and ends the talk."
	buttons := []string{menuPresenter, menuAudience, menuSaveCopy}
	for _, tool := range dialogTools {
		if _, err := lookPath(tool); err != nil {
			continue
		}
		var args []string
		switch tool {
		case "zenity":
			args = []string{"--title=" + brand.Name, "--info", "--no-markup", "--width=460",
				"--ok-label=Stop", "--text=" + zenityText(msg, false)}
			for _, b := range buttons {
				args = append(args, "--extra-button="+b)
			}
		case "kdialog":
			// Three buttons only: Yes, No and Cancel.
			args = []string{"--title", brand.Name, "--yes-label", buttons[0], "--no-label", buttons[2],
				"--cancel-label", "Stop", "--yesnocancel", msg}
		}
		out, code, err := runControl(tool, args, started)
		if err != nil {
			log.Printf("%s: %v", tool, err)
			continue
		}
		switch {
		case tool == "kdialog" && code == 0:
			return buttons[0], true
		case tool == "kdialog" && code == 1:
			return buttons[2], true
		case tool == "kdialog" && code == 2:
			return "", true
		case tool == "zenity" && (code == 0 || code == 1):
			return strings.TrimSpace(out), true
		}
		log.Printf("%s could not show the window (exit code %d)", tool, code)
	}
	return "", false
}

// runControl runs the window's program, handing started a function that closes
// it while it is up; tests replace it.
var runControl = func(name string, args []string, started func(close func())) (out string, code int, err error) {
	var stdout bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdout = &stdout
	if err := cmd.Start(); err != nil {
		return "", 0, err
	}
	started(func() { cmd.Process.Kill() })
	err = cmd.Wait()
	started(nil)
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return stdout.String(), ee.ExitCode(), nil
	}
	return stdout.String(), 0, err
}
