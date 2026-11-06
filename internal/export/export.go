package export

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"stagepack/internal/app"
	"stagepack/internal/brand"
	"stagepack/internal/browser"
	"stagepack/internal/instance"
	"stagepack/internal/slidev"
	"stagepack/internal/ui"
	"stagepack/internal/workspace"
	slidevrt "stagepack/runtime"
)

// Dialogs are what an export shows the person: PlatformDialogs on the
// desktop, HeadlessDialogs as JSON lines for tests and scripts.
type Dialogs interface {
	// Confirm asks a yes/no question, No the default; true is yes.
	Confirm(title, msg string) bool
	// AskSavePath asks where to save the .pptx, starting at suggest, and
	// returns a path the person agreed to write, having been asked about an
	// existing file; false when cancelled or refused.
	AskSavePath(suggest string) (string, bool)
	Error(title, msg string)
	ShowProgress(title, head string) ui.Progress
	// Finished shows msg with buttons and returns the index of the one pressed, or -1.
	Finished(title, msg string, buttons []string) int
}

// Options say what to export and how.
type Options struct {
	Home       string // the viewer's folder: node/, runtimes/, instances/, logs/, …
	DeckPath   string
	Animations bool    // the export with animations, else the editable one
	Dialogs    Dialogs // default PlatformDialogs()
	GOOS       string  // default runtime.GOOS
	// TrustCode runs a presentation's code without asking (scripts, tests). It
	// does not add it to the trusted list.
	TrustCode bool
	// Replace replaces an existing .pptx without asking.
	Replace bool
	// Run runs the exporter; default Run.
	Run func(ctx context.Context, cmd *exec.Cmd, out string, onEvent func(Event)) error
	// FindBrowser finds the Chrome, Edge or Chromium the export draws with;
	// default browser.FindAppBrowser on this computer.
	FindBrowser func() (string, bool)
	// Open opens the finished file (default browser.Open), and Reveal shows
	// it in its folder (default the file manager), for the finished box's
	// buttons.
	Open, Reveal func(path string) error
}

// Result is a finished export.
type Result struct {
	Path          string
	Slides, Clips int
	Bytes         int64
	Notes         []string // what the finished box says besides the numbers
}

var (
	// ErrBusy is returned when the file is being exported already.
	ErrBusy = errors.New("already exporting")
	// ErrCancelled is returned when the person said no to replacing the file,
	// didn't say where to save it, or cancelled the export; nothing was written.
	ErrCancelled = errors.New("export cancelled")
	// ErrNoBrowser is returned when no Chrome, Edge or Chromium was found;
	// nothing was unpacked or written.
	ErrNoBrowser = errors.New("no Chrome, Edge or Chromium found")
	// ErrNotTrusted is returned when the person chose not to run the
	// presentation's code; nothing was run. It is app.ErrNotTrusted to errors.Is.
	ErrNotTrusted error = notTrusted{}
)

type notTrusted struct{}

func (notTrusted) Error() string {
	return "not exported: the presentation contains code and was not allowed to run it"
}

func (notTrusted) Is(target error) bool { return target == app.ErrNotTrusted }

// readHeadmatter is what the code check runs Slidev's parser with (app.CodeCheck); tests replace it.
var readHeadmatter = slidev.ReadHeadmatter

const maxNotes = 3 // notices the finished box shows

// Export exports the deck at o.DeckPath to <name>.pptx, or <name>-animations.pptx,
// next to it. It shows the person what happens, errors included, and returns
// ErrBusy, ErrCancelled, ErrNotTrusted or the export's failure. The work
// folder and the lock are removed in every case.
func Export(ctx context.Context, o Options) (Result, error) {
	if o.Dialogs == nil {
		o.Dialogs = PlatformDialogs()
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.Run == nil {
		o.Run = Run
	}
	if o.FindBrowser == nil {
		o.FindBrowser = func() (string, bool) {
			return browser.FindAppBrowser(runtime.GOOS, os.Getenv, exec.LookPath)
		}
	}
	if o.Open == nil {
		o.Open = browser.Open
	}
	if o.Reveal == nil {
		o.Reveal = reveal
	}
	d := o.Dialogs
	home, err := filepath.Abs(o.Home)
	if err != nil {
		return Result{}, err
	}
	deckPath, err := filepath.Abs(o.DeckPath)
	if err != nil {
		return Result{}, err
	}
	file := filepath.Base(deckPath)

	// 0. The browser comes first: without one nothing is taken, unpacked or written.
	chrome, ok := o.FindBrowser()
	if !ok {
		if o.GOOS == "windows" {
			d.Error(brand.Name, "Exporting to PowerPoint needs Microsoft Edge or Chrome.")
		} else {
			d.Error(brand.Name, "Exporting to PowerPoint needs Chrome, Edge or Chromium. Install one and try again.")
		}
		return Result{}, ErrNoBrowser
	}

	// 1. One export of a file at a time.
	unlock, err := lock(home, deckPath)
	if errors.Is(err, ErrBusy) {
		d.Error(brand.Name, "Already exporting "+file+".")
		return Result{}, fmt.Errorf("%w %s", ErrBusy, file)
	}
	if err != nil {
		d.Error(brand.Name, fmt.Sprintf("Couldn't export %s: %s.", file, firstLine(err.Error())))
		return Result{}, err
	}
	var work workspace.Work
	cleanup := sync.OnceFunc(func() {
		if work.Dir != "" {
			if err := workspace.Remove(work); err != nil {
				log.Printf("remove the work folder: %v", err)
			}
		}
		unlock()
	})
	defer cleanup()

	// 2. Where the file goes.
	out, err := output(d, deckPath, o.Animations, o.Replace)
	if err != nil {
		return Result{}, err
	}

	// 3. The box, up while the presentation is unpacked; its Cancel ends rctx.
	tracker := NewTracker(file, o.Animations, time.Now)
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	show := func() ui.Progress {
		b := d.ShowProgress(brand.Name, tracker.Head())
		b.Update(tracker.Step())
		go func() {
			select {
			case <-b.Cancelled():
				cancel()
			case <-rctx.Done():
			}
		}()
		return b
	}
	box := show()
	defer func() { box.Close() }()
	fail := func(msg string, err error) (Result, error) {
		box.Close()
		cleanup()
		d.Error(brand.Name, msg)
		return Result{}, err
	}
	rtDir := filepath.Join(home, "runtimes", strconv.Itoa(slidevrt.Major()))
	if work, err = workspace.Extract(deckPath, rtDir); err != nil {
		return fail(fmt.Sprintf("Couldn't export %s: %s.", file, firstLine(err.Error())), err)
	}
	tracker.Unpacked()
	box.Update(tracker.Step())

	// 4. The code it brought along runs only with the person's yes, given here
	// or when it was opened (app.CodeCheck).
	paths := PathsFor(home, o.GOOS)
	paths.Browser = chrome
	check := app.CodeCheck{Home: home, Node: paths.Node, RuntimeDir: rtDir, TrustCode: o.TrustCode, ReadHeadmatter: readHeadmatter,
		Ask: func(question string) bool {
			if rctx.Err() != nil {
				return false // cancelled while unpacking: there is nothing to ask about
			}
			box.Close()
			if !d.Confirm(brand.Name, question) {
				return false
			}
			box = show()
			return true
		}}
	allowed := check.Allow(rctx, &work, deckPath)
	if rctx.Err() != nil {
		return Result{}, ErrCancelled
	}
	if !allowed {
		log.Printf("%s not exported: its code was not allowed to run", deckPath)
		return Result{}, ErrNotTrusted
	}

	// 5. Run the exporter; what it says for people goes to the log.
	cmd := Command(paths, Job{Slides: filepath.Join(work.Dir, work.Manifest.Entry), Out: out, Animations: o.Animations})
	logDir := filepath.Join(home, "logs")
	lf, err := openLog(filepath.Join(logDir, "export-"+instance.Key(deckPath)+".log"), deckPath, out)
	if err != nil {
		log.Printf("export log: %v", err)
	} else {
		cmd.Stderr = lf
	}
	start := time.Now().Truncate(2 * time.Second) // FAT keeps file times in steps of 2 s
	var done *Event
	var notices []string
	err = o.Run(rctx, cmd, out, func(e Event) {
		tracker.Apply(e)
		box.Update(tracker.Step())
		switch e.Event {
		case "notice":
			notices = append(notices, e.Text)
		case "done":
			done = &e
		}
	})
	if err != nil && lf != nil {
		fmt.Fprintf(lf, "stagepack: %v\n", err)
	}
	if lf != nil {
		lf.Close()
	}

	// 6. Without done, leave only what was there before: no .partial, and no
	// file this export renamed into place just before a cancel or a crash.
	if done == nil {
		os.Remove(out + ".partial")
		if fi, serr := os.Stat(out); serr == nil && !fi.ModTime().Before(start) {
			os.Remove(out)
		}
		if rctx.Err() != nil {
			return Result{}, ErrCancelled
		}
		if err == nil {
			err = errors.New("the exporter ended without finishing the file")
		}
		log.Printf("export %s: %v", deckPath, err)
		return fail(failureText(file, filepath.Base(out), logDir, err.Error()), fmt.Errorf("export %s: %w", file, err))
	}
	if err != nil {
		log.Printf("export %s: done, then %v", deckPath, err) // the file is complete
	}
	box.Close()
	cleanup() // before the finished box, which may stay up a long time
	res := Result{Path: out, Slides: done.Slides, Clips: done.Clips, Bytes: done.Bytes, Notes: notes(notices)}
	name := filepath.Base(out)
	switch d.Finished("Exported "+name, summary(res), []string{"Open", "Show in folder", "Close"}) {
	case 0:
		err = o.Open(out)
	case 1:
		err = o.Reveal(out)
	default:
		err = nil
	}
	if err != nil {
		d.Error(brand.Name, fmt.Sprintf("Couldn't show %s: %s.", name, firstLine(err.Error())))
	}
	return res, nil
}

// lock takes <home>/instances/<instance.Key(deck)>.export, made exclusively
// and holding this process's PID. A lock whose PID no longer runs was left by
// a killed export and is taken over; any other is ErrBusy.
func lock(home, deckPath string) (unlock func(), err error) {
	dir := filepath.Join(home, "instances")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, instance.Key(deckPath)+".export")
	pid := strconv.Itoa(os.Getpid())
	for range 2 {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if errors.Is(err, os.ErrExist) {
			if !takeOver(p) {
				return nil, ErrBusy
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		_, werr := f.WriteString(pid)
		if err := errors.Join(werr, f.Close()); err != nil {
			os.Remove(p)
			return nil, err
		}
		return func() {
			if data, err := os.ReadFile(p); err == nil && string(data) == pid {
				os.Remove(p)
			}
		}, nil
	}
	return nil, ErrBusy
}

// staleLock is the age at which a lock counts as left behind even though a
// process with its PID runs: that PID has been reused.
const staleLock = 6 * time.Hour

// takeOver removes the lock at p if it was left behind, and reports whether
// it did: its PID no longer runs, or it is older than staleLock. A lock
// without a PID is being written this moment, unless it is older than 10 s:
// then its export was killed right after making it.
func takeOver(p string) bool {
	fi, err := os.Stat(p)
	if err != nil {
		return errors.Is(err, os.ErrNotExist)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return errors.Is(err, os.ErrNotExist)
	}
	age := time.Since(fi.ModTime())
	pid, perr := strconv.Atoi(string(data))
	switch {
	case age > staleLock:
	case perr != nil && age < 10*time.Second:
		return false
	case perr == nil && pid > 0 && instance.PIDRunning(pid):
		return false
	}
	// Moved aside first: of two exports taking over at once, the one that
	// finds the other's new lock there puts it back.
	aside := p + "." + strconv.Itoa(os.Getpid())
	if os.Rename(p, aside) != nil {
		return false
	}
	now, _ := os.ReadFile(aside)
	if afi, err := os.Stat(aside); err != nil || !afi.ModTime().Equal(fi.ModTime()) || !bytes.Equal(now, data) {
		os.Rename(aside, p)
		return false
	}
	os.Remove(aside)
	return true
}

// output is where the .pptx goes: <name>.pptx or <name>-animations.pptx next
// to the deck, an existing one replaced only after a yes (or with replace).
// Where the deck's folder can't be written, the person is asked where to save
// instead; the save box asks itself before replacing a file.
func output(d Dialogs, deckPath string, animations, replace bool) (string, error) {
	base := filepath.Base(deckPath)
	if ext := filepath.Ext(base); strings.EqualFold(ext, brand.Ext) {
		base = strings.TrimSuffix(base, ext)
	}
	if animations {
		base += "-animations"
	}
	name := base + ".pptx"
	out := filepath.Join(filepath.Dir(deckPath), name)
	if writable(filepath.Dir(out)) {
		if _, err := os.Stat(out); err == nil && !replace && !d.Confirm(brand.Name, "Replace "+name+"?") {
			return "", ErrCancelled
		}
		return out, nil
	}
	suggest := name
	if h, err := os.UserHomeDir(); err == nil {
		suggest = filepath.Join(h, name)
	}
	p, ok := d.AskSavePath(suggest)
	if !ok || p == "" {
		return "", ErrCancelled
	}
	if !strings.EqualFold(filepath.Ext(p), ".pptx") {
		// The person agreed to the name without .pptx: ask about the one with it.
		p += ".pptx"
		if _, err := os.Stat(p); err == nil && !replace && !d.Confirm(brand.Name, "Replace "+filepath.Base(p)+"?") {
			return "", ErrCancelled
		}
	}
	p, err := filepath.Abs(p)
	if err == nil && !writable(filepath.Dir(p)) {
		err = fmt.Errorf("%s can't be written to", filepath.Dir(p))
	}
	if err != nil {
		d.Error(brand.Name, fmt.Sprintf("Couldn't export %s: %s.", filepath.Base(deckPath), firstLine(err.Error())))
		return "", err
	}
	return p, nil
}

// writable reports whether a file can be made in dir, by making one.
func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".stagepack-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// openLog starts the export's log at p, replacing the last one of this deck.
func openLog(p, deckPath, out string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(p)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(f, "stagepack: %s: exporting %s to %s\n", time.Now().Format(time.RFC3339), deckPath, out)
	return f, nil
}

// failureText is the one sentence a failed export shows. The exporter renames
// its file into place at the end, which Windows refuses while PowerPoint has
// the file there open.
func failureText(file, name, logDir, msg string) string {
	low := strings.ToLower(msg)
	for _, s := range []string{"ebusy", "eperm", "resource busy", "operation not permitted", "being used by another process"} {
		if strings.Contains(low, s) {
			return fmt.Sprintf("Couldn't export %s: %s may be open in PowerPoint; close it and try again.", file, name)
		}
	}
	return fmt.Sprintf("Couldn't export %s: %s. The log is in %s.", file, firstLine(msg), logDir)
}

// firstLine is msg's first line without a full stop at its end.
func firstLine(msg string) string {
	msg, _, _ = strings.Cut(strings.TrimSpace(msg), "\n")
	if msg = strings.TrimRight(strings.TrimSpace(msg), "."); msg == "" {
		return "it failed"
	}
	return msg
}

// notes is what the finished box says of the exporter's notices: up to three
// of them, leaving out each clip's size, and the parts exported as pictures
// as a count.
func notes(notices []string) []string {
	var out []string
	pictures := 0
	for _, n := range notices {
		switch {
		case strings.Contains(n, "clip, ") && strings.HasSuffix(n, " MB."):
		case strings.Contains(n, "exported as a picture"):
			pictures++
		case len(out) < maxNotes:
			out = append(out, n)
		}
	}
	switch {
	case pictures == 1:
		out = append(out, "1 part is a picture")
	case pictures > 1:
		out = append(out, fmt.Sprintf("%d parts are pictures", pictures))
	}
	return out
}

// summary is the finished box's text: "23 slides, 11 with a video, 29.9 MB",
// then the notes.
func summary(r Result) string {
	slides := fmt.Sprintf("%d slides", r.Slides)
	if r.Slides == 1 {
		slides = "1 slide"
	}
	msg := fmt.Sprintf("%s, %d with a video, %.1f MB", slides, r.Clips, float64(r.Bytes)/(1<<20))
	if len(r.Notes) > 0 {
		msg += "\n\n" + strings.Join(r.Notes, "\n")
	}
	return msg
}

// PlatformDialogs shows the export's dialogs on the desktop.
func PlatformDialogs() Dialogs { return platformDialogs{} }

type platformDialogs struct{}

func (platformDialogs) Confirm(title, msg string) bool { return ui.ConfirmDefaultNo(title, msg) }

func (platformDialogs) AskSavePath(suggest string) (string, bool) {
	p, ok, err := ui.SaveAs("Export to PowerPoint", suggest, "PowerPoint presentation", ".pptx")
	if err != nil {
		ui.Alert(brand.Name, err.Error())
	}
	return p, ok
}

func (platformDialogs) Error(title, msg string) { ui.Alert(title, msg) }

func (platformDialogs) ShowProgress(title, head string) ui.Progress {
	return ui.ShowProgress(title, head)
}

func (platformDialogs) Finished(title, msg string, buttons []string) int {
	return ui.Finished(title, msg, buttons)
}

// HeadlessDialogs writes one JSON line per dialog to w, for tests and scripts:
// {"event":"confirm",…,"answer":"no"} (nobody can say yes), {"event":"save",
// "path":…} (the suggestion is taken; with "answer":"no" it is refused, being
// an existing file and replace not set), {"event":"error",…}, the progress
// lines of ui.NewHeadlessProgress and the finished line of ui.HeadlessFinished.
// replace is the person's agreement, given beforehand (--yes), to replace a
// file AskSavePath suggests.
func HeadlessDialogs(w io.Writer, replace bool) Dialogs {
	return &headlessDialogs{w: w, replace: replace}
}

type headlessDialogs struct {
	mu      sync.Mutex
	w       io.Writer
	replace bool
}

// Write writes p to w whole, so lines from the box and the dialogs never mix.
func (h *headlessDialogs) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.w.Write(p)
}

func (h *headlessDialogs) emit(event, title, msg, path, answer string) {
	data, err := json.Marshal(struct {
		Event   string `json:"event"`
		Title   string `json:"title,omitempty"`
		Message string `json:"message,omitempty"`
		Path    string `json:"path,omitempty"`
		Answer  string `json:"answer,omitempty"`
	}{event, title, msg, path, answer})
	if err == nil {
		h.Write(append(data, '\n'))
	}
}

func (h *headlessDialogs) Confirm(title, msg string) bool {
	h.emit("confirm", title, msg, "", "no")
	return false
}

func (h *headlessDialogs) AskSavePath(suggest string) (string, bool) {
	if _, err := os.Stat(suggest); err == nil && !h.replace {
		h.emit("save", "", "", suggest, "no")
		return "", false
	}
	h.emit("save", "", "", suggest, "")
	return suggest, true
}

func (h *headlessDialogs) Error(title, msg string) { h.emit("error", title, msg, "", "") }

func (h *headlessDialogs) ShowProgress(title, head string) ui.Progress {
	return ui.NewHeadlessProgress(h)
}

func (h *headlessDialogs) Finished(title, msg string, buttons []string) int {
	return ui.HeadlessFinished(h, title, msg, buttons)
}
