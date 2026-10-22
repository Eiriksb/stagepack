package ui

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFallbackWindowWithoutATray(t *testing.T) {
	oldArea, oldControl := trayAreaPresent, runControl
	t.Cleanup(func() { trayAreaPresent, runControl = oldArea, oldControl })
	trayAreaPresent = func() bool { return false }
	f := &fakeTools{installed: []string{"zenity"}}
	withTools(t, f)

	var mu sync.Mutex
	var did []string
	record := func(s string) func() {
		return func() { mu.Lock(); did = append(did, s); mu.Unlock() }
	}
	var tr *tray
	tr = NewTray(Actions{
		OpenPresenter: record("presenter"),
		OpenAudience:  record("audience"),
		SaveCopy:      record("save"),
		Stop:          func() { record("stop")(); tr.Quit() },
	}).(*tray)
	tr.SetStatus("Talk", 0, "")

	// The person picks Open presenter, then Save a copy…, then closes the window.
	answers := []string{menuPresenter + "\n", menuSaveCopy + "\n", ""}
	var windows []string
	runControl = func(name string, args []string, started func(func())) (string, int, error) {
		windows = append(windows, name+" "+strings.Join(args, " "))
		started(func() {})
		a := answers[0]
		answers = answers[1:]
		return a, 1, nil
	}
	readied := make(chan struct{})
	ended := make(chan struct{})
	go func() { tr.Run(func() { close(readied) }); close(ended) }()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not end after the window was closed")
	}
	<-readied
	time.Sleep(10 * time.Millisecond) // the presenter opens on its own goroutine
	mu.Lock()
	defer mu.Unlock()
	slices.Sort(did)
	if want := []string{"presenter", "save", "stop"}; !slices.Equal(did, want) {
		t.Errorf("actions = %q, want %q", did, want)
	}
	if len(windows) != 3 || !strings.Contains(windows[0], "--ok-label=Stop") || !strings.Contains(windows[0], "--extra-button="+menuPresenter) {
		t.Errorf("windows = %q", windows)
	}
}

func TestFallbackWindowClosesOnQuit(t *testing.T) {
	oldArea, oldControl := trayAreaPresent, runControl
	t.Cleanup(func() { trayAreaPresent, runControl = oldArea, oldControl })
	trayAreaPresent = func() bool { return false }
	withTools(t, &fakeTools{installed: []string{"zenity"}})
	closed := make(chan struct{})
	runControl = func(name string, args []string, started func(func())) (string, int, error) {
		started(func() { close(closed) })
		<-closed // up until closed, which a kill does
		return "", 1, nil
	}
	tr := NewTray(Actions{Stop: func() { t.Error("closing for Quit counted as Stop") }}).(*tray)
	ended := make(chan struct{})
	go func() { tr.Run(func() {}); close(ended) }()
	time.Sleep(20 * time.Millisecond)
	tr.Quit()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("Quit did not end Run")
	}
}
