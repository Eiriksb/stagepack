package ui

import (
	"encoding/json"
	"io"
	"log"
	"math"
	"sync"
)

// Progress is a box that shows how a long piece of work is going, with a
// Cancel button. ShowProgress opens one (progress_unix.go, progress_windows.go).
type Progress interface {
	// Update shows the current step, such as "Slide 4 of 11 · about 3 min
	// left", and how much is done, from 0 to 1. It does not block; a box
	// that is slow to redraw shows only the latest.
	Update(step string, fraction float64)
	// Cancelled is closed when the person cancels, by the button or by
	// closing the box. It never fires after Close.
	Cancelled() <-chan struct{}
	// Close takes the box away, and is not a cancel. It is safe to call
	// more than once, and after a cancel.
	Close()
}

// percent is fraction as a whole percentage, from 0 to 100.
func percent(fraction float64) int {
	if !(fraction > 0) { // and NaN
		return 0
	}
	return int(math.Round(math.Min(fraction, 1) * 100))
}

// headlessProgress writes one JSON line per Update.
type headlessProgress struct {
	mu     sync.Mutex
	w      io.Writer
	closed bool
	never  chan struct{}
}

// NewHeadlessProgress returns a Progress that writes
// {"event":"progress","step":…,"fraction":…} to w for each Update until Close,
// for tests and scripts. Nobody can cancel it.
func NewHeadlessProgress(w io.Writer) Progress {
	return &headlessProgress{w: w, never: make(chan struct{})}
}

func (h *headlessProgress) Update(step string, fraction float64) {
	data, err := json.Marshal(struct {
		Event    string  `json:"event"`
		Step     string  `json:"step"`
		Fraction float64 `json:"fraction"`
	}{"progress", step, fraction})
	if err != nil { // NaN or ±Inf
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.closed {
		h.w.Write(append(data, '\n'))
	}
}

func (h *headlessProgress) Cancelled() <-chan struct{} { return h.never }

func (h *headlessProgress) Close() {
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
}

// HeadlessFinished writes {"event":"finished","title":…,"message":…,"buttons":[…]}
// to w and returns -1, as nobody can press a button.
func HeadlessFinished(w io.Writer, title, msg string, buttons []string) int {
	data, err := json.Marshal(struct {
		Event   string   `json:"event"`
		Title   string   `json:"title"`
		Message string   `json:"message"`
		Buttons []string `json:"buttons"`
	}{"finished", title, msg, buttons})
	if err == nil {
		w.Write(append(data, '\n'))
	}
	return -1
}

// loggedProgress stands in for a box where none can be shown: each new step
// goes to the log, and nobody can cancel.
type loggedProgress struct {
	mu    sync.Mutex
	title string
	last  string
	never chan struct{}
}

func newLoggedProgress(title string) *loggedProgress {
	return &loggedProgress{title: title, never: make(chan struct{})}
}

func (l *loggedProgress) Update(step string, fraction float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if step != l.last {
		l.last = step
		log.Printf("%s: %s (%d%%)", l.title, step, percent(fraction))
	}
}

func (l *loggedProgress) Cancelled() <-chan struct{} { return l.never }

func (l *loggedProgress) Close() {}
