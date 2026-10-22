package ui

import (
	"encoding/json"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"stagepack/internal/save"
)

// headless writes one JSON line per call, for tests and scripts.
type headless struct {
	a    Actions
	mu   sync.Mutex
	w    io.Writer
	quit chan struct{}
	once sync.Once
}

// NewHeadless returns a UI that writes one JSON line per call to w, such as
// {"event":"status","status":"saved","title":"…"}. Its Run stops on Quit, or
// on SIGINT or SIGTERM after calling a.Stop. AskSavePath accepts the suggestion.
func NewHeadless(w io.Writer, a Actions) UI {
	return &headless{a: a, w: w, quit: make(chan struct{})}
}

type line struct {
	Event   string `json:"event"`
	Status  string `json:"status,omitempty"`
	Title   string `json:"title,omitempty"`
	Message string `json:"message,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

func (h *headless) emit(l line) {
	data, err := json.Marshal(l)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.w.Write(append(data, '\n'))
}

func (h *headless) Run(ready func()) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	ready()
	select {
	case <-h.quit:
	case <-sig:
		if h.a.Stop != nil {
			h.a.Stop()
		}
		h.Quit()
	}
}

func (h *headless) SetStatus(title string, s save.Status, detail string) {
	h.emit(line{Event: "status", Status: s.String(), Title: title, Detail: detail})
}

// Opening writes {"event":"opening","title":name}; done writes nothing.
func (h *headless) Opening(name string) (done func()) {
	h.emit(line{Event: "opening", Title: name})
	return func() {}
}

func (h *headless) Notify(title, msg string) {
	h.emit(line{Event: "notify", Title: title, Message: msg})
}

func (h *headless) Error(title, msg string) {
	h.emit(line{Event: "error", Title: title, Message: msg})
}

func (h *headless) AskSavePath(suggest string) (string, bool) { return suggest, true }

func (h *headless) Quit() { h.once.Do(func() { close(h.quit) }) }
