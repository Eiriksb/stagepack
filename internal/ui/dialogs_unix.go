//go:build !windows

package ui

// Dialogs on Linux desktops: the zenity program (GNOME), else kdialog (KDE),
// each run as a separate process, so no GUI library is linked. Where neither
// can show a dialog, messages go to the log and to notify-send, and a question
// is answered no.

import (
	"bytes"
	"errors"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// dialogKind is what a dialog is for.
type dialogKind int

const (
	kindInfo dialogKind = iota
	kindError
	kindQuestion // yes or no; No is the default
	kindSave     // a file name to save to
	kindBusy     // shown until the program closes it
)

type dialog struct {
	kind       dialogKind
	title, msg string
	suggest    string // kindSave: the suggested path
	filterName string // kindSave: "Stagepack talk"
	ext        string // kindSave: ".sptx"
}

// dialogTools are the programs that can show dialogs, in the order they are tried.
var dialogTools = []string{"zenity", "kdialog"}

// zenityText is s for zenity's --text, which reads backslash escapes (and, in a
// progress dialog, which has no --no-markup, Pango markup).
func zenityText(s string, markup bool) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	if markup {
		s = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
	}
	return s
}

// dialogArgs is the command line for tool to show d; nil if it can't.
func dialogArgs(tool string, d dialog) []string {
	switch tool {
	case "zenity":
		a := []string{"--title=" + d.title}
		switch d.kind {
		case kindInfo:
			return append(a, "--info", "--no-markup", "--width=460", "--text="+zenityText(d.msg, false))
		case kindError:
			return append(a, "--error", "--no-markup", "--width=460", "--text="+zenityText(d.msg, false))
		case kindQuestion:
			return append(a, "--question", "--no-markup", "--width=560", "--default-cancel",
				"--ok-label=Yes", "--cancel-label=No", "--text="+zenityText(d.msg, false))
		case kindSave:
			return append(a, "--file-selection", "--save", "--confirm-overwrite",
				"--filename="+d.suggest, "--file-filter="+d.filterName+" | *"+d.ext)
		case kindBusy:
			return append(a, "--progress", "--pulsate", "--no-cancel", "--auto-close", "--width=360",
				"--text="+zenityText(d.msg, true))
		}
	case "kdialog":
		a := []string{"--title", d.title}
		switch d.kind {
		case kindInfo:
			return append(a, "--msgbox", d.msg)
		case kindError:
			return append(a, "--error", d.msg)
		case kindQuestion:
			return append(a, "--warningyesno", d.msg)
		case kindSave:
			return append(a, "--getsavefilename", d.suggest, "*"+d.ext+"|"+d.filterName)
		case kindBusy:
			return append(a, "--passivepopup", d.msg, "60")
		}
	}
	return nil
}

// The seams to the outside; tests replace them.
var (
	lookPath = exec.LookPath
	// runDialog runs a dialog program and returns what it printed and its exit code.
	runDialog = func(name string, args []string) (out string, code int, err error) {
		var stdout bytes.Buffer
		cmd := exec.Command(name, args...)
		cmd.Stdout = &stdout
		err = cmd.Run()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return stdout.String(), ee.ExitCode(), nil
		}
		return stdout.String(), 0, err
	}
	// startDialog starts a dialog program and returns a function that closes it.
	// Its input stays open until then: zenity's progress dialog ends when its input does.
	startDialog = func(name string, args []string) (stop func(), err error) {
		cmd := exec.Command(name, args...)
		in, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		ended := make(chan struct{})
		go func() { cmd.Wait(); close(ended) }()
		return sync.OnceFunc(func() {
			in.Close()
			select {
			case <-ended:
			case <-time.After(2 * time.Second):
				cmd.Process.Kill()
				<-ended
			}
		}), nil
	}
)

// show shows d with the first dialog program that can, and returns what it
// printed and its exit code: 0 for OK or Yes, 1 for No, Cancel or a closed
// window. ok is false when no program could show it.
func show(d dialog) (out string, code int, ok bool) {
	for _, tool := range dialogTools {
		if _, err := lookPath(tool); err != nil {
			continue
		}
		out, code, err := runDialog(tool, dialogArgs(tool, d))
		if err != nil {
			log.Printf("%s: %v", tool, err)
			continue
		}
		if code == 0 || code == 1 {
			return out, code, true
		}
		// It ran, but could not show the dialog (no display, an option it doesn't know).
		log.Printf("%s could not show a dialog (exit code %d)", tool, code)
	}
	return "", 0, false
}

// notifySend shows msg as a desktop notification, if notify-send is there.
func notifySend(title, msg string) {
	if _, err := lookPath("notify-send"); err != nil {
		return
	}
	if _, code, err := runDialog("notify-send", []string{"--app-name=" + title, "--", title, msg}); err != nil || code != 0 {
		log.Printf("notify-send: exit code %d, %v", code, err)
	}
}

// message shows an information or error message and returns when it is closed;
// without a dialog program it goes to the log and to a notification.
func message(kind dialogKind, title, msg string) {
	if _, _, ok := show(dialog{kind: kind, title: title, msg: msg}); ok {
		return
	}
	log.Printf("%s: %s", title, msg)
	notifySend(title, msg)
}

func infoBox(title, msg string) { message(kindInfo, title, msg) }

func errorBox(title, msg string) { message(kindError, title, msg) }

// confirmBox asks a yes/no question, No the default. Without a dialog program
// nobody can be asked, and the answer is no.
func confirmBox(title, msg string) bool {
	_, code, ok := show(dialog{kind: kindQuestion, title: title, msg: msg})
	if !ok {
		log.Printf("%s: neither zenity nor kdialog can show a dialog here, so the question below "+
			"could not be asked and the answer is no (install zenity to be asked):\n%s", title, msg)
		notifySend(title, "A question could not be asked, so the answer was no. Install zenity to be asked.")
		return false
	}
	return code == 0
}

// saveBox asks for a file to save to, starting at suggest. ok is false when cancelled.
func saveBox(title, suggest, filterName, ext string) (string, bool, error) {
	out, code, ok := show(dialog{kind: kindSave, title: title, suggest: suggest, filterName: filterName, ext: ext})
	if !ok {
		return "", false, errors.New("cannot ask where to save: neither zenity nor kdialog can show a dialog here (install zenity)")
	}
	p := strings.TrimRight(out, "\r\n")
	if code != 0 || p == "" {
		return "", false, nil
	}
	return p, true, nil
}

// Confirm asks a yes/no question and reports whether the answer is yes.
func Confirm(title, msg string) bool { return confirmBox(title, msg) }

// Notice shows msg and returns when it is closed.
func Notice(title, msg string) { infoBox(title, msg) }

// Alert shows an error message and returns when it is closed.
func Alert(title, msg string) { errorBox(title, msg) }

// Busy shows msg without blocking the caller, until done is called. Without a
// dialog program it is a notification.
func Busy(title, msg string) (done func()) {
	d := dialog{kind: kindBusy, title: title, msg: msg}
	for _, tool := range dialogTools {
		if _, err := lookPath(tool); err != nil {
			continue
		}
		stop, err := startDialog(tool, dialogArgs(tool, d))
		if err != nil {
			log.Printf("%s: %v", tool, err)
			continue
		}
		return stop
	}
	notifySend(title, msg)
	return func() {}
}
