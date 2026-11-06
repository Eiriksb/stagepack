//go:build !windows

package ui

// The progress box and the finished box on Linux desktops: zenity, else
// kdialog, each a separate process as for the other dialogs; kdialog's
// progress box is driven over D-Bus with qdbus or gdbus. Without either, or
// without a display, a notification says the work has started, the steps go
// to the log, and nobody can cancel.

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// haveDisplay reports whether a window can be shown at all. Without a display
// zenity ends at once with 1, which is also what Cancel gives.
func haveDisplay() bool { return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "" }

// ShowProgress opens a progress box called title, with head as its first line
// of text and a Cancel button, and returns without waiting.
func ShowProgress(title, head string) Progress {
	if haveDisplay() {
		for _, tool := range dialogTools {
			if _, err := lookPath(tool); err != nil {
				continue
			}
			var p Progress
			var err error
			switch tool {
			case "zenity":
				p, err = startZenityProgress(title, head)
			case "kdialog":
				p, err = startKdialogProgress(title, head)
			default:
				continue
			}
			if err == nil {
				return p
			}
			log.Printf("%s: %v", tool, err)
		}
	}
	notifySend(title, head)
	return newLoggedProgress(title)
}

// boxText is the text of a progress box: the head line, then the step.
func boxText(head, step string) string {
	switch {
	case head == "":
		return step
	case step == "":
		return head
	}
	return head + "\n" + step
}

// zenityLine is s for a "# text" line on the input of zenity's progress box,
// which must stay one line, and in which zenity reads backslash escapes (so a
// line break is \n). It is plain text, not markup.
func zenityLine(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\r", `\r`).Replace(s)
}

// zenityProgress is zenity's progress box, which reads "# text" and percentage
// lines on its input. It has Cancel (no --no-cancel), and no --auto-close: it
// stays up when its input ends or the bar is full, so that zenity ending before
// Close is the person cancelling. Close ends it.
type zenityProgress struct {
	head string
	in   io.WriteCloser
	proc *os.Process

	mu      sync.Mutex
	step    string
	pct     int
	closing bool // set by Close: zenity ending is then not a cancel

	kick      chan struct{} // an Update waits to be written
	stop      chan struct{} // closed by Close
	exited    chan struct{} // closed once zenity has ended
	written   chan struct{} // closed once the writer has stopped
	cancelled chan struct{}
	once      sync.Once
}

func startZenityProgress(title, head string) (*zenityProgress, error) {
	cmd := exec.Command("zenity", "--progress", "--title="+title, "--text="+zenityText(head, true),
		"--percentage=0", "--width=420")
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &zenityProgress{head: head, in: in, proc: cmd.Process,
		kick: make(chan struct{}, 1), stop: make(chan struct{}), exited: make(chan struct{}),
		written: make(chan struct{}), cancelled: make(chan struct{})}
	go p.wait(cmd)
	go p.write()
	return p, nil
}

// wait waits for zenity to end, which before Close is a cancel when it ends
// with 1: Cancel, Escape or the window closed.
func (p *zenityProgress) wait(cmd *exec.Cmd) {
	err := cmd.Wait() // which also closes its input
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode() // -1 when killed
	} else if err != nil {
		code = -1
	}
	p.mu.Lock()
	switch {
	case p.closing:
	case code == 1:
		close(p.cancelled)
	case code == 0:
		// OK, which zenity offers once the bar is full: the box is gone, the work goes on.
	default:
		log.Printf("zenity's progress box ended (exit code %d); the work goes on without it", code)
	}
	p.mu.Unlock()
	close(p.exited)
}

// write writes the latest Update to zenity's input whenever there is a new one.
func (p *zenityProgress) write() {
	defer close(p.written)
	var last string
	for {
		select {
		case <-p.kick:
		case <-p.stop:
			return
		case <-p.exited:
			return
		}
		p.mu.Lock()
		s := "# " + zenityLine(boxText(p.head, p.step)) + "\n" + strconv.Itoa(p.pct) + "\n"
		p.mu.Unlock()
		if s == last {
			continue
		}
		last = s
		if _, err := io.WriteString(p.in, s); err != nil {
			return // zenity has ended
		}
	}
}

func (p *zenityProgress) Update(step string, fraction float64) {
	p.mu.Lock()
	p.step, p.pct = step, percent(fraction)
	p.mu.Unlock()
	select {
	case p.kick <- struct{}{}:
	default: // the writer will pick this one up with the one already waiting
	}
}

func (p *zenityProgress) Cancelled() <-chan struct{} { return p.cancelled }

func (p *zenityProgress) Close() {
	p.once.Do(func() {
		p.mu.Lock()
		p.closing = true
		p.mu.Unlock()
		close(p.stop)
		p.proc.Signal(syscall.SIGTERM) // an error if it has ended already
		select {
		case <-p.exited:
		case <-time.After(2 * time.Second):
			p.proc.Kill()
			<-p.exited
		}
		<-p.written // a write blocked on zenity fails once it has ended
	})
}

// kdialogPoll is how often kdialog's progress box is brought up to date and
// asked whether it was cancelled.
var kdialogPoll = 300 * time.Millisecond

// kdialogExitWait is how long a box that can't be reached is given to end.
var kdialogExitWait = time.Second

// kdialogIface is the D-Bus interface of kdialog's progress box.
const kdialogIface = "org.kde.kdialog.ProgressDialog"

// kdialogBox is kdialog's progress box, reached over D-Bus with the program
// tool (qdbus or gdbus) at the service and path kdialog printed.
type kdialogBox struct{ tool, service, path string }

// call calls method on the box; a method without a dot is the box's own.
func (b kdialogBox) call(method string, args ...string) (string, error) {
	var a []string
	switch b.tool {
	case "qdbus":
		a = append([]string{b.service, b.path, method}, args...)
	default: // gdbus
		if !strings.Contains(method, ".") {
			method = kdialogIface + "." + method
		}
		a = append([]string{"call", "--session", "--dest", b.service, "--object-path", b.path, "--method", method}, args...)
	}
	out, code, err := runDialog(b.tool, a)
	if err == nil && code != 0 {
		err = fmt.Errorf("%s %s: exit code %d", b.tool, method, code)
	}
	return out, err
}

func (b kdialogBox) setValue(pct int) error {
	var err error
	if b.tool == "qdbus" {
		_, err = b.call("Set", "", "value", strconv.Itoa(pct))
	} else {
		_, err = b.call("org.freedesktop.DBus.Properties.Set", gvariantString(kdialogIface), gvariantString("value"),
			fmt.Sprintf("<int32 %d>", pct))
	}
	return err
}

func (b kdialogBox) setLabel(text string) error {
	if b.tool != "qdbus" {
		text = gvariantString(text)
	}
	_, err := b.call("setLabelText", text)
	return err
}

// wasCancelled asks the box; qdbus answers true, gdbus (true,).
func (b kdialogBox) wasCancelled() (bool, error) {
	out, err := b.call("wasCancelled")
	return strings.Contains(out, "true"), err
}

// gvariantString is s as a GVariant text string, which is how gdbus reads its
// arguments.
func gvariantString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`).Replace(s) + `"`
}

// kdialogProgress is kdialog's progress box, which kdialog leaves running on
// its own once it has printed where to reach it. A loop sends it the latest
// Update and asks it whether it was cancelled.
type kdialogProgress struct {
	box  kdialogBox
	head string

	mu      sync.Mutex
	step    string
	pct     int
	closing bool

	stop      chan struct{} // closed by Close
	done      chan struct{} // closed once the loop has stopped
	cancelled chan struct{}
	once      sync.Once
}

func startKdialogProgress(title, head string) (*kdialogProgress, error) {
	var tools []string
	for _, t := range []string{"qdbus", "gdbus"} {
		if _, err := lookPath(t); err == nil {
			tools = append(tools, t)
		}
	}
	if len(tools) == 0 {
		return nil, errors.New("its progress box needs qdbus or gdbus, and neither is there")
	}
	out, code, err := runDialog("kdialog", []string{"--title", title, "--progressbar", head, "100"})
	if err != nil {
		return nil, err
	}
	ref := strings.Fields(out) // "org.kde.kdialog-1234 /ProgressDialog"
	if code != 0 || len(ref) != 2 {
		return nil, fmt.Errorf("could not show a progress box (exit code %d, %q)", code, out)
	}
	// The box may not be on the bus yet when kdialog has printed where it will be: try for a second.
	for try := 0; try < 10; try++ {
		if try > 0 {
			time.Sleep(100 * time.Millisecond)
		}
		for _, t := range tools {
			box := kdialogBox{t, ref[0], ref[1]}
			if _, err = box.call("showCancelButton", "true"); err != nil {
				continue
			}
			p := &kdialogProgress{box: box, head: head, stop: make(chan struct{}), done: make(chan struct{}),
				cancelled: make(chan struct{})}
			go p.loop()
			return p, nil
		}
	}
	return nil, fmt.Errorf("could not reach its progress box over D-Bus: %v", err)
}

func (p *kdialogProgress) loop() {
	defer close(p.done)
	tick := time.NewTicker(kdialogPoll)
	defer tick.Stop()
	shownStep, shownPct := "", -1
	for {
		select {
		case <-p.stop:
			return
		case <-tick.C:
		}
		p.mu.Lock()
		step, pct := p.step, p.pct
		p.mu.Unlock()
		var err error
		if pct != shownPct {
			err = p.box.setValue(pct)
			shownPct = pct
		}
		if err == nil && step != shownStep {
			err = p.box.setLabel(boxText(p.head, step))
			shownStep = step
		}
		var cancelled bool
		if err == nil {
			cancelled, err = p.box.wasCancelled()
		}
		if err != nil && p.boxEnded() {
			cancelled, err = true, nil // Cancel, Escape or the close button end the box
		}
		if err != nil {
			log.Printf("kdialog's progress box can't be reached (%v); the work goes on without it", err)
			return
		}
		if cancelled {
			p.mu.Lock()
			if !p.closing {
				close(p.cancelled)
			}
			p.mu.Unlock()
			return
		}
	}
}

// boxEnded reports whether kdialog's progress box has ended, giving it
// kdialogExitWait to. The helper that shows it ends when Cancel, Escape or
// the close button hides it, and its D-Bus name, which holds its process
// number, goes with it.
func (p *kdialogProgress) boxEnded() bool {
	pid, err := strconv.Atoi(strings.TrimPrefix(p.box.service, "org.kde.kdialog-"))
	if err != nil || pid <= 0 { // kill(0) and kill(-1) would ask about other processes
		return false
	}
	deadline := time.After(kdialogExitWait)
	for {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return true
		}
		select {
		case <-p.stop: // Close: not a cancel, whatever it is
			return false
		case <-deadline:
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (p *kdialogProgress) Update(step string, fraction float64) {
	p.mu.Lock()
	p.step, p.pct = step, percent(fraction)
	p.mu.Unlock()
}

func (p *kdialogProgress) Cancelled() <-chan struct{} { return p.cancelled }

func (p *kdialogProgress) Close() {
	p.once.Do(func() {
		p.mu.Lock()
		p.closing = true
		p.mu.Unlock()
		close(p.stop)
		<-p.done
		if _, err := p.box.call("close"); err != nil {
			log.Printf("kdialog: %v", err)
		}
	})
}

// Finished shows msg with buttons, waits until one is pressed and returns its
// index; -1 when the box is closed, or when no box can be shown (msg is then a
// notification).
func Finished(title, msg string, buttons []string) int {
	if haveDisplay() {
		for _, tool := range dialogTools {
			if _, err := lookPath(tool); err != nil {
				continue
			}
			out, code, err := runDialog(tool, finishedArgs(tool, title, msg, buttons))
			if err != nil {
				log.Printf("%s: %v", tool, err)
				continue
			}
			if i, ok := finishedAnswer(tool, out, code, buttons); ok {
				return i
			}
			// It ran, but could not show the box (no display, an option it doesn't know).
			log.Printf("%s could not show a dialog (exit code %d)", tool, code)
		}
	}
	log.Printf("%s: %s", title, msg)
	notifySend(title, msg)
	return -1
}

// finishedArgs is the command line for tool to show the finished box. zenity
// has an OK button, which is the last button, and extra buttons for the
// others; kdialog has Yes, No and Cancel, which are the first, the second and
// the last button (any between are left out).
func finishedArgs(tool, title, msg string, buttons []string) []string {
	n := len(buttons)
	switch tool {
	case "zenity":
		a := []string{"--info", "--title=" + title, "--no-markup", "--width=460", "--text=" + zenityText(msg, false)}
		if n > 0 {
			a = append(a, "--ok-label="+buttons[n-1])
			for _, b := range buttons[:n-1] {
				a = append(a, "--extra-button="+b)
			}
		}
		return a
	case "kdialog":
		a := []string{"--title", title}
		switch {
		case n < 2:
			return append(a, "--msgbox", msg)
		case n == 2:
			return append(a, "--yes-label", buttons[0], "--no-label", buttons[1], "--yesno", msg)
		}
		return append(a, "--yes-label", buttons[0], "--no-label", buttons[1], "--cancel-label", buttons[n-1],
			"--yesnocancel", msg)
	}
	return nil
}

// finishedAnswer is the index of the button tool's exit code and output name;
// ok is false when the code says the box could not be shown.
func finishedAnswer(tool, out string, code int, buttons []string) (i int, ok bool) {
	n := len(buttons)
	switch tool {
	case "zenity":
		switch {
		case code == 0:
			return n - 1, true // OK; -1 without buttons
		case code == 1 && n > 1:
			// An extra button, whose label zenity prints; nothing for a closed window.
			return slices.Index(buttons[:n-1], strings.TrimRight(out, "\r\n")), true
		case code == 1:
			return -1, true
		}
	case "kdialog":
		switch {
		case code == 0:
			return min(0, n-1), true // Yes or OK; -1 without buttons
		case code == 1 && n >= 2:
			return 1, true
		case code == 2 && n >= 3:
			return n - 1, true // Cancel, which closing the window gives too
		}
	}
	return 0, false
}
