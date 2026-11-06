package ui

// The progress box and the finished box on Windows: Task Dialogs
// (TaskDialogIndirect), which only comctl32.dll version 6 has; the program's
// manifest asks for it (build/mkwinres). Without it (go test, go run), the
// steps go to the log and the finished box is a message box.

import (
	"encoding/binary"
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"stagepack/internal/brand"
)

var (
	comctl32                = windows.NewLazySystemDLL("comctl32.dll")
	procTaskDialogIndirect  = comctl32.NewProc("TaskDialogIndirect")
	procSendMessageW        = user32.NewProc("SendMessageW")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
)

const (
	tdfAllowDialogCancellation = 0x0008 // TDF_ALLOW_DIALOG_CANCELLATION
	tdfShowProgressBar         = 0x0200 // TDF_SHOW_PROGRESS_BAR
	tdfCallbackTimer           = 0x0800 // TDF_CALLBACK_TIMER: TDN_TIMER about every 200 ms
	tdcbfCancelButton          = 0x0008 // TDCBF_CANCEL_BUTTON

	tdnCreated       = 0 // TDN_CREATED
	tdnButtonClicked = 2 // TDN_BUTTON_CLICKED
	tdnTimer         = 4 // TDN_TIMER
	tdnDestroyed     = 5 // TDN_DESTROYED

	wmUser               = 0x0400
	tdmClickButton       = wmUser + 102 // TDM_CLICK_BUTTON
	tdmSetProgressBarPos = wmUser + 106 // TDM_SET_PROGRESS_BAR_POS
	tdmUpdateElementText = wmUser + 114 // TDM_UPDATE_ELEMENT_TEXT
	tdeContent           = 0            // TDE_CONTENT

	idCancel      = 2   // IDCANCEL: the Cancel button, Escape and the window's close button
	firstButtonID = 100 // the finished box's buttons are 100, 101, …

	// progressWidth is the progress box's width in dialog units, enough for a
	// step such as "Slide 12 of 120, recording click 12 · about 15 min left" on
	// one line: TDM_UPDATE_ELEMENT_TEXT doesn't make the box any bigger.
	progressWidth = 280
)

// taskDialogConfig is what runTaskDialog shows.
type taskDialogConfig struct {
	flags, commonButtons                  uint32
	windowTitle, mainInstruction, content string
	buttons                               []string // custom buttons, firstButtonID onwards
	defaultButton                         int32
	width                                 uint32 // dialog units; 0 for the dialog's own
}

// taskDialog is what a Task Dialog reads while it is up: TASKDIALOGCONFIG and
// its TASKDIALOG_BUTTONs, packed to 1 byte as in commctrl.h, and the strings
// they point to. It stays in taskDialogs until the dialog is gone, so none of
// it is freed meanwhile (and the Go heap doesn't move).
type taskDialog struct {
	config, buttons []byte
	strs            [][]uint16
	notify          func(hwnd uintptr, msg uint32, wparam uintptr) uintptr
}

var (
	taskDialogsMu sync.Mutex
	taskDialogs   = map[uintptr]*taskDialog{} // by lpCallbackData
	taskDialogSeq uintptr
	// taskDialogCallback is every Task Dialog's TaskDialogCallbackProc, as
	// callbacks are few: it hands each notification to its dialog's notify.
	taskDialogCallback = syscall.NewCallback(func(hwnd, msg, wparam, _, ref uintptr) uintptr {
		taskDialogsMu.Lock()
		td := taskDialogs[ref]
		taskDialogsMu.Unlock()
		if td == nil || td.notify == nil {
			return 0 // S_OK
		}
		return td.notify(hwnd, uint32(msg), wparam)
	})
)

// packed is a C struct packed to 1 byte, little-endian.
type packed []byte

func (p *packed) u32(v uint32) { *p = binary.LittleEndian.AppendUint32(*p, v) }

func (p *packed) ptr(v uintptr) {
	if unsafe.Sizeof(v) == 8 {
		*p = binary.LittleEndian.AppendUint64(*p, uint64(v))
	} else {
		p.u32(uint32(v))
	}
}

// str is s as UTF-16 that td keeps; 0 for "".
func (td *taskDialog) str(s string) uintptr {
	if s == "" {
		return 0
	}
	u, err := windows.UTF16FromString(strings.ReplaceAll(s, "\x00", ""))
	if err != nil {
		return 0
	}
	td.strs = append(td.strs, u)
	return uintptr(unsafe.Pointer(&u[0]))
}

// runTaskDialog shows c until it closes and returns the ID of the button that
// closed it. It must be called on a locked OS thread; notify is called on it,
// with each TDN_ notification, and returns S_OK (0) to let a button close it.
func runTaskDialog(c taskDialogConfig, notify func(hwnd uintptr, msg uint32, wparam uintptr) uintptr) (int32, error) {
	td := &taskDialog{notify: notify}
	taskDialogsMu.Lock()
	taskDialogSeq++
	ref := taskDialogSeq
	taskDialogs[ref] = td
	taskDialogsMu.Unlock()
	defer func() {
		taskDialogsMu.Lock()
		delete(taskDialogs, ref)
		taskDialogsMu.Unlock()
	}()

	var b packed
	for i, label := range c.buttons {
		b.u32(uint32(firstButtonID + i)) // nButtonID
		b.ptr(td.str(label))             // pszButtonText
	}
	td.buttons = b
	var buttons uintptr
	if len(td.buttons) > 0 {
		buttons = uintptr(unsafe.Pointer(&td.buttons[0]))
	}

	var p packed
	p.u32(0) // cbSize, below
	p.ptr(0) // hwndParent
	p.ptr(0) // hInstance
	p.u32(c.flags)
	p.u32(c.commonButtons)
	p.ptr(td.str(c.windowTitle))
	p.ptr(0) // hMainIcon
	p.ptr(td.str(c.mainInstruction))
	p.ptr(td.str(c.content))
	p.u32(uint32(len(c.buttons)))
	p.ptr(buttons)
	p.u32(uint32(c.defaultButton))
	p.u32(0) // cRadioButtons
	p.ptr(0) // pRadioButtons
	p.u32(0) // nDefaultRadioButton
	p.ptr(0) // pszVerificationText
	p.ptr(0) // pszExpandedInformation
	p.ptr(0) // pszExpandedControlText
	p.ptr(0) // pszCollapsedControlText
	p.ptr(0) // hFooterIcon
	p.ptr(0) // pszFooter
	p.ptr(taskDialogCallback)
	p.ptr(ref) // lpCallbackData
	p.u32(c.width)
	binary.LittleEndian.PutUint32(p, uint32(len(p))) // 160 bytes on 64-bit Windows, 96 on 32-bit
	td.config = p

	var button int32
	hr, _, _ := procTaskDialogIndirect.Call(uintptr(unsafe.Pointer(&td.config[0])), uintptr(unsafe.Pointer(&button)), 0, 0)
	runtime.KeepAlive(td)
	if hr != 0 {
		return 0, fmt.Errorf("TaskDialogIndirect failed (HRESULT 0x%08x)", uint32(hr))
	}
	return button, nil
}

// taskProgress is a Task Dialog with a bar and Cancel. Update only stores the
// step; the dialog's timer shows the latest, on the dialog's own thread.
type taskProgress struct {
	mu      sync.Mutex
	hwnd    uintptr // while the box is up
	step    string
	pct     int
	dirty   bool // an Update not shown yet
	closing bool // set by Close: a button click is then done, not a cancel

	cancelled chan struct{}
	ended     chan struct{} // closed once the box is gone
	once      sync.Once
}

// ShowProgress opens a progress box called title, with head as its main line
// and a Cancel button, and returns without waiting.
func ShowProgress(title, head string) Progress {
	if err := procTaskDialogIndirect.Find(); err != nil {
		log.Printf("no progress box, as comctl32.dll version 6 is not loaded: %v", err)
		return newLoggedProgress(title)
	}
	p := &taskProgress{cancelled: make(chan struct{}), ended: make(chan struct{})}
	go func() {
		defer close(p.ended)
		runtime.LockOSThread() // the box, and its callback, belong to this thread
		defer runtime.UnlockOSThread()
		_, err := runTaskDialog(taskDialogConfig{
			flags:           tdfShowProgressBar | tdfCallbackTimer,
			commonButtons:   tdcbfCancelButton,
			windowTitle:     title,
			mainInstruction: head,
			content:         " ", // TDM_UPDATE_ELEMENT_TEXT changes only text that is there
			width:           progressWidth,
		}, p.notify)
		if err != nil {
			log.Printf("progress box: %v", err)
		}
	}()
	return p
}

func (p *taskProgress) notify(hwnd uintptr, msg uint32, _ uintptr) uintptr {
	switch msg {
	case tdnCreated:
		procSetForegroundWindow.Call(hwnd)
		p.mu.Lock()
		p.hwnd = hwnd
		closing := p.closing
		p.mu.Unlock()
		if closing { // Close came before the box was up
			procPostMessageW.Call(hwnd, tdmClickButton, idCancel, 0)
			return 0
		}
		p.show(hwnd)
	case tdnTimer:
		p.show(hwnd)
	case tdnButtonClicked:
		// Cancel, Escape or the close button, unless Close clicked it.
		p.mu.Lock()
		if !p.closing {
			select {
			case <-p.cancelled:
			default:
				close(p.cancelled)
			}
		}
		p.mu.Unlock()
	case tdnDestroyed:
		p.mu.Lock()
		p.hwnd = 0
		p.mu.Unlock()
	}
	return 0 // S_OK: a click closes the box
}

// show shows the latest Update, if it isn't shown yet.
func (p *taskProgress) show(hwnd uintptr) {
	p.mu.Lock()
	step, pct, dirty := p.step, p.pct, p.dirty
	p.dirty = false
	p.mu.Unlock()
	if !dirty {
		return
	}
	procSendMessageW.Call(hwnd, tdmSetProgressBarPos, uintptr(pct), 0)
	if step == "" {
		step = " "
	}
	if s, err := windows.UTF16PtrFromString(strings.ReplaceAll(step, "\x00", "")); err == nil {
		procSendMessageW.Call(hwnd, tdmUpdateElementText, tdeContent, uintptr(unsafe.Pointer(s)))
	}
}

func (p *taskProgress) Update(step string, fraction float64) {
	p.mu.Lock()
	p.step, p.pct, p.dirty = step, percent(fraction), true
	p.mu.Unlock()
}

func (p *taskProgress) Cancelled() <-chan struct{} { return p.cancelled }

// Close clicks the box's Cancel button with closing set, so that the callback
// takes the click for done. (A made-up button ID would be clearer, but what
// TDM_CLICK_BUTTON does with an ID the box hasn't got is not documented.)
func (p *taskProgress) Close() {
	p.once.Do(func() {
		p.mu.Lock()
		p.closing = true
		hwnd := p.hwnd
		p.mu.Unlock()
		if hwnd != 0 { // else TDN_CREATED clicks it
			procPostMessageW.Call(hwnd, tdmClickButton, idCancel, 0)
		}
		select {
		case <-p.ended:
		case <-time.After(5 * time.Second):
			log.Printf("the progress box did not close")
		}
	})
}

// Finished shows msg under title with one button per label, waits until one is
// pressed and returns its index; -1 when the box is closed.
func Finished(title, msg string, buttons []string) int {
	if err := procTaskDialogIndirect.Find(); err != nil {
		infoBox(title, msg)
		return -1
	}
	runtime.LockOSThread() // the box, and its callback, belong to this thread
	defer runtime.UnlockOSThread()
	b, err := runTaskDialog(taskDialogConfig{
		flags:           tdfAllowDialogCancellation,
		windowTitle:     brand.Name,
		mainInstruction: title,
		content:         msg,
		buttons:         buttons,
		defaultButton:   firstButtonID,
	}, func(hwnd uintptr, n uint32, _ uintptr) uintptr {
		if n == tdnCreated {
			procSetForegroundWindow.Call(hwnd)
		}
		return 0
	})
	if err != nil {
		log.Printf("finished box: %v", err)
		infoBox(title, msg)
		return -1
	}
	if i := int(b) - firstButtonID; i >= 0 && i < len(buttons) {
		return i
	}
	return -1
}
