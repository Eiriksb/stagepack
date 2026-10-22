package ui

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Plain Win32 dialogs: MessageBoxW for errors and notices, GetSaveFileNameW
// for "Save a copy…".

var (
	comdlg32                 = windows.NewLazySystemDLL("comdlg32.dll")
	procGetSaveFileNameW     = comdlg32.NewProc("GetSaveFileNameW")
	procCommDlgExtendedError = comdlg32.NewProc("CommDlgExtendedError")
)

// messageBox shows a modal message box and returns when it is closed.
func messageBox(title, msg string, icon uint32) {
	messageBoxButtons(title, msg, windows.MB_OK|icon)
}

// messageBoxButtons shows a modal message box with the given MB_ flags and
// returns the button pressed (IDOK, IDYES, ...), 0 if it could not be shown.
func messageBoxButtons(title, msg string, flags uint32) int32 {
	t, err1 := windows.UTF16PtrFromString(title)
	m, err2 := windows.UTF16PtrFromString(msg)
	if err1 != nil || err2 != nil { // a NUL in the text
		return 0
	}
	r, _ := windows.MessageBox(0, m, t, flags|windows.MB_TOPMOST|windows.MB_SETFOREGROUND)
	return r
}

const (
	idYes   = 6      // IDYES
	wmClose = 0x0010 // WM_CLOSE
)

// infoBox, errorBox, confirmBox and saveBox are the tray's dialogs (tray.go).

func infoBox(title, msg string) { messageBox(title, msg, windows.MB_ICONINFORMATION) }

func errorBox(title, msg string) { messageBox(title, msg, windows.MB_ICONERROR) }

// confirmBox shows a warning with Yes and No, No the default.
func confirmBox(title, msg string) bool {
	return messageBoxButtons(title, msg, windows.MB_YESNO|windows.MB_ICONWARNING|windows.MB_DEFBUTTON2) == idYes
}

func saveBox(title, suggest, filterName, ext string) (string, bool, error) {
	return saveFileDialog(title, suggest, filterName, ext)
}

// Confirm asks a yes/no question and reports whether the answer is yes.
func Confirm(title, msg string) bool {
	return messageBoxButtons(title, msg, windows.MB_YESNO|windows.MB_ICONQUESTION) == idYes
}

// Notice shows msg with an OK button and returns when it is closed.
func Notice(title, msg string) { messageBox(title, msg, windows.MB_ICONINFORMATION) }

// Alert shows an error message with an OK button and returns when it is closed.
func Alert(title, msg string) { messageBox(title, msg, windows.MB_ICONERROR) }

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procEnumThreadWindows   = user32.NewProc("EnumThreadWindows")
	procPostMessageW        = user32.NewProc("PostMessageW")
	enumThreadWindowsAction = syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		procPostMessageW.Call(hwnd, wmClose, 0, 0)
		return 1 // go on to the thread's other windows
	})
)

// Busy shows an OK-only notice without blocking the caller, and returns a
// function that closes it again. The person may close it earlier; the work goes on.
func Busy(title, msg string) (done func()) {
	tid := make(chan uint32, 1)
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		runtime.LockOSThread() // the box belongs to this thread; its window is found by thread
		defer runtime.UnlockOSThread()
		tid <- windows.GetCurrentThreadId()
		messageBox(title, msg, windows.MB_ICONINFORMATION)
	}()
	id := <-tid
	return func() {
		// The box may not be up yet when the work is already over: ask until it is gone.
		for i := 0; i < 100; i++ {
			select {
			case <-ended:
				return
			case <-time.After(20 * time.Millisecond):
			}
			procEnumThreadWindows.Call(uintptr(id), enumThreadWindowsAction, 0)
		}
	}
}

// openFileName is OPENFILENAMEW.
type openFileName struct {
	structSize    uint32
	owner         uintptr
	instance      uintptr
	filter        *uint16
	customFilter  *uint16
	maxCustFilter uint32
	filterIndex   uint32
	file          *uint16
	maxFile       uint32
	fileTitle     *uint16
	maxFileTitle  uint32
	initialDir    *uint16
	title         *uint16
	flags         uint32
	fileOffset    uint16
	fileExtension uint16
	defExt        *uint16
	custData      uintptr
	hook          uintptr
	templateName  *uint16
	reserved      uintptr
	reservedDword uint32
	flagsEx       uint32
}

const (
	ofnOverwritePrompt = 0x00000002
	ofnNoChangeDir     = 0x00000008
	ofnPathMustExist   = 0x00000800
	ofnExplorer        = 0x00080000
)

// saveFileDialog asks for a file to save, starting at suggest and offering
// files ending in ext (such as ".deck"). ok is false when cancelled.
func saveFileDialog(title, suggest, filterName, ext string) (path string, ok bool, err error) {
	// The dialog uses COM; give it a thread of its own in a single-threaded apartment.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// S_FALSE (already initialised here) also needs its CoUninitialize.
	if hr := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); hr == nil || hr == syscall.Errno(1) {
		defer windows.CoUninitialize()
	}

	pattern := "*" + ext
	filter := utf16z(filterName + " (" + pattern + ")\x00" + pattern + "\x00\x00")
	buf := make([]uint16, 32768)
	copy(buf[:len(buf)-1], windows.StringToUTF16(filepath.Base(suggest)))
	ofn := openFileName{
		filter:      &filter[0],
		filterIndex: 1,
		file:        &buf[0],
		maxFile:     uint32(len(buf)),
		initialDir:  ptr(filepath.Dir(suggest)),
		title:       ptr(title),
		flags:       ofnOverwritePrompt | ofnNoChangeDir | ofnPathMustExist | ofnExplorer,
		defExt:      ptr(strings.TrimPrefix(ext, ".")),
	}
	ofn.structSize = uint32(unsafe.Sizeof(ofn))
	r, _, _ := procGetSaveFileNameW.Call(uintptr(unsafe.Pointer(&ofn)))
	if r == 0 {
		if code, _, _ := procCommDlgExtendedError.Call(); code != 0 {
			return "", false, fmt.Errorf("save dialog failed (CommDlgExtendedError 0x%x)", code)
		}
		return "", false, nil // cancelled
	}
	return windows.UTF16ToString(buf), true, nil
}

// utf16z converts s, which may hold NULs, to UTF-16 without adding a terminator.
func utf16z(s string) []uint16 {
	var out []uint16
	for _, part := range strings.Split(s, "\x00") {
		out = append(out, windows.StringToUTF16(part)...) // each part gets its NUL
	}
	return out[:len(out)-1] // the split adds one NUL too many
}

func ptr(s string) *uint16 {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return nil
	}
	return p
}
