package ui

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"sync"

	"fyne.io/systray"
	"golang.org/x/sys/windows"

	"stagepack/internal/brand"
	"stagepack/internal/save"
)

// tray is a notification-area icon whose menu shows the status and the actions.
// Dialogs and notices are plain Win32 message boxes (dialogs_windows.go).
type tray struct {
	a Actions

	mu      sync.Mutex
	status  *systray.MenuItem // nil until the tray is up
	text    string            // status line
	tip     string            // its detail
	shown   save.Status       // last status, to notify only on a change into Failed/Throwaway
	started bool              // Run was called
	quit    sync.Once
}

// NewTray returns the Windows tray UI: a disabled status line, then
// "Open presenter", "Open audience view", "Save a copy…" and "Stop".
func NewTray(a Actions) UI { return &tray{a: a, shown: -1} }

func (t *tray) Run(ready func()) {
	t.mu.Lock()
	t.started = true
	t.mu.Unlock()
	systray.Run(func() {
		systray.SetIcon(trayIcon())
		systray.SetTooltip(brand.Name)
		t.mu.Lock()
		t.status = systray.AddMenuItem(t.text, t.tip)
		t.status.Disable()
		t.mu.Unlock()
		systray.AddSeparator()
		items := []struct {
			m  *systray.MenuItem
			fn func()
		}{
			{systray.AddMenuItem("Open presenter", ""), t.a.OpenPresenter},
			{systray.AddMenuItem("Open audience view", ""), t.a.OpenAudience},
			{systray.AddMenuItem("Save a copy…", "Save the talk with your notes somewhere else"), t.a.SaveCopy},
			{systray.AddMenuItem("Stop", "Save the notes and stop the talk"), t.a.Stop},
		}
		for _, it := range items {
			go func(ch <-chan struct{}, fn func()) {
				for range ch {
					if fn != nil {
						fn()
					}
				}
			}(it.m.ClickedCh, it.fn)
		}
		ready()
	}, nil)
}

// statusText is the menu's status line.
func statusText(title string, s save.Status, detail string) string {
	switch s {
	case save.Saved:
		return title + " · Notes saved"
	case save.Pending:
		if detail != "" {
			return detail // "Starting <title>…"
		}
		return title + " · Saving…"
	case save.Failed:
		return title + " · Notes not saved in the file"
	case save.Throwaway:
		return title + " · Temporary copy: notes won't be kept"
	}
	return title
}

func (t *tray) SetStatus(title string, s save.Status, detail string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.text, t.tip = statusText(title, s, detail), detail
	if t.status != nil {
		t.status.SetTitle(t.text)
		t.status.SetTooltip(t.tip)
		systray.SetTooltip(t.text)
	}
	if s != t.shown && (s == save.Failed || s == save.Throwaway) && detail != "" {
		// Not blocking: SetStatus may be called under the save watcher's lock.
		go messageBox(title, detail, windows.MB_ICONINFORMATION)
	}
	t.shown = s
}

// Opening shows the "Opening <name>…" box until done is called.
func (t *tray) Opening(name string) (done func()) {
	return Busy(brand.Name, "Opening "+name+"…")
}

// Notify shows an information box. Once the tray runs, it does so from its own
// goroutine, so it never blocks. Before that it waits for the box to be closed:
// the program may end right after (a second launch of a talk that is still
// opening), which would take the box away unseen.
func (t *tray) Notify(title, msg string) {
	t.mu.Lock()
	started := t.started
	t.mu.Unlock()
	if !started {
		messageBox(title, msg, windows.MB_ICONINFORMATION)
		return
	}
	go messageBox(title, msg, windows.MB_ICONINFORMATION)
}

// Error shows an error box and returns when it is closed.
func (t *tray) Error(title, msg string) {
	messageBox(title, msg, windows.MB_ICONERROR)
}

// Confirm shows a warning with Yes and No, No the default, and returns when it is answered.
func (t *tray) Confirm(title, msg string) bool {
	return messageBoxButtons(title, msg, windows.MB_YESNO|windows.MB_ICONWARNING|windows.MB_DEFBUTTON2) == idYes
}

func (t *tray) AskSavePath(suggest string) (string, bool) {
	p, ok, err := saveFileDialog("Save a copy", suggest, brand.Name+" talk", brand.Ext)
	if err != nil {
		t.Error(brand.Name, err.Error())
	}
	return p, ok
}

func (t *tray) Quit() { t.quit.Do(systray.Quit) }

// trayIcon is a 32×32 .ico holding a PNG: a rounded blue tile with a white
// "play" triangle. Drawn here so no binary asset is needed.
func trayIcon() []byte {
	const n = 32
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	blue := color.NRGBA{0x25, 0x63, 0xeb, 0xff}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if inRounded(x, y, n, 6) {
				img.Set(x, y, blue)
			}
			// triangle pointing right: x from 11 to 23, centred on y = 16
			if x >= 11 && x <= 23 {
				half := float64(23-x) * 0.6
				if float64(y) >= 15.5-half && float64(y) <= 15.5+half {
					img.Set(x, y, color.NRGBA{0xff, 0xff, 0xff, 0xff})
				}
			}
		}
	}
	var p bytes.Buffer
	png.Encode(&p, img)
	var ico bytes.Buffer
	le := binary.LittleEndian
	binary.Write(&ico, le, [3]uint16{0, 1, 1})                         // reserved, type icon, one image
	ico.Write([]byte{n, n, 0, 0})                                      // width, height, colours, reserved
	binary.Write(&ico, le, [2]uint16{1, 32})                           // planes, bits per pixel
	binary.Write(&ico, le, [2]uint32{uint32(p.Len()), uint32(6 + 16)}) // size, offset
	ico.Write(p.Bytes())
	return ico.Bytes()
}

// inRounded reports whether (x, y) is inside an n×n square with corner radius r.
func inRounded(x, y, n, r int) bool {
	cx, cy := x, y
	if x < r {
		cx = r
	} else if x >= n-r {
		cx = n - r - 1
	}
	if y < r {
		cy = r
	} else if y >= n-r {
		cy = n - r - 1
	}
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= r*r
}
