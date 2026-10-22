// Package install puts the viewer on a Windows user's machine and takes it off
// again: it unpacks the bundled Node and Slidev runtime into a per-user folder,
// copies the program there and registers the file type under HKCU, so that no
// administrator rights are needed.
//
// Everything that is not a Win32 call is plain Go, so it is tested on Linux.
package install

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"stagepack/internal/brand"
	"stagepack/internal/trust"
)

// RegValue is one registry value. Name "" is the key's default value. A value
// whose name isDWORD is written as a DWORD, the others as strings.
type RegValue struct{ Key, Name, Value string }

const (
	classesKey = `HKCU\Software\Classes\`
	// Only these two HKCU subtrees are ever written or deleted.
	uninstallKeyBase = `HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\`
)

var (
	extKey       = classesKey + brand.Ext
	progKey      = classesKey + brand.ProgID
	uninstallKey = uninstallKeyBase + brand.Name
)

// ExeName is the program's file name inside the install folder.
func ExeName() string { return strings.ToLower(brand.Name) + ".exe" }

// isDWORD says whether the Windows writer stores the value of name as a DWORD
// (the plan carries it as its decimal string).
func isDWORD(name string) bool { return name == "NoModify" || name == "NoRepair" }

// RegistryPlan lists what Install registers for the program at exe, whose icon
// is the first icon of the file icon. Paths are Windows paths, whatever the OS
// this runs on.
func RegistryPlan(exe, icon string) []RegValue {
	return []RegValue{
		{extKey, "", brand.ProgID},
		{progKey, "", brand.Name + " talk"},
		{progKey + `\DefaultIcon`, "", `"` + icon + `"`},
		{progKey + `\shell\open\command`, "", `"` + exe + `" open "%1"`},
		{uninstallKey, "DisplayName", brand.Name},
		{uninstallKey, "UninstallString", `"` + exe + `" uninstall`},
		{uninstallKey, "InstallLocation", winDir(exe)},
		{uninstallKey, "NoModify", "1"},
		{uninstallKey, "NoRepair", "1"},
	}
}

// winDir is the folder part of a Windows path.
func winDir(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[:i]
	}
	return "."
}

// assoc is the Windows-only part: file association and shell notification.
type assoc interface {
	write(plan []RegValue) error
	// remove deletes what write created.
	remove() error
	// notify tells Explorer that associations changed, so the icon shows at once.
	notify()
}

// ErrNotInstalled is returned by Uninstall for a folder this program did not install into.
var ErrNotInstalled = errors.New("not an install folder")

// markerName is the file Install leaves in the install folder, so that an uninstall,
// given any folder, deletes only where this program was installed.
func markerName() string { return "." + strings.ToLower(brand.Name) + "-install" }

// IsInstalled says whether home holds an install: the marker and the program.
func IsInstalled(home string) bool {
	_, err1 := os.Stat(filepath.Join(home, markerName()))
	_, err2 := os.Stat(filepath.Join(home, ExeName()))
	return err1 == nil && err2 == nil
}

const (
	stagingName = ".install-new" // the new files, before they replace the old
	trashName   = ".install-old" // the old files, until the new ones are in place
	journalName = "journal"
)

// Install unpacks payload into home, puts the program there (from the payload
// if it holds one, else a copy of the running program) and registers the file type. On a reinstall node/ and runtimes/ are replaced and
// the journal (unsaved notes) is kept, as are the folders of decks being shown.
func Install(home string, payload fs.FS) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find this program: %w", err)
	}
	return install(home, payload, self, platformRegistry)
}

func install(home string, payload fs.FS, self string, reg assoc) error {
	home, err := filepath.Abs(home) // the registry must not hold a relative path
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	if err := installFiles(home, payload); err != nil {
		return err
	}
	exe := filepath.Join(home, ExeName())
	// The payload may carry the program itself (a lean build, without the payload
	// inside it); else the running program is copied, payload and all.
	if _, err := fs.Stat(payload, ExeName()); err != nil {
		if err := copyProgram(self, exe); err != nil {
			return fmt.Errorf("copy the program: %w", err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, markerName()), []byte(brand.Name+" install folder: Uninstall deletes only here.\n"), 0o644); err != nil {
		return fmt.Errorf("write the install marker: %w", err)
	}
	if err := reg.write(RegistryPlan(exe, exe)); err != nil {
		return fmt.Errorf("register %s files: %w", brand.Ext, err)
	}
	reg.notify()
	return nil
}

// installFiles unpacks payload into home: each top-level entry of the payload
// replaces the one in home. The new files are written beside, and swapped in
// only when all of them are on disk, so a failed reinstall leaves the old
// install in place.
func installFiles(home string, payload fs.FS) (err error) {
	top, err := fs.ReadDir(payload, ".")
	if err != nil {
		return fmt.Errorf("read the bundled files: %w", err)
	}
	for _, e := range top {
		if e.Name() == journalName || strings.HasPrefix(e.Name(), ".install-") {
			return fmt.Errorf("the bundled files hold a reserved name: %s", e.Name())
		}
	}
	staging := filepath.Join(home, stagingName)
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	// Leftovers of an earlier install: a program that was running could not be deleted then.
	// A new trash folder each time, so a leftover that is still locked is no obstacle.
	if old, _ := filepath.Glob(filepath.Join(home, trashName+"*")); len(old) > 0 {
		for _, d := range old {
			os.RemoveAll(d)
		}
	}
	trash := filepath.Join(home, fmt.Sprintf("%s-%d", trashName, time.Now().UnixNano()))
	defer os.RemoveAll(staging)
	if err := unpack(payload, staging); err != nil {
		return err
	}

	// Swap. Moving the old folder away fails while a program in it is running
	// (a talk is open), which is when a reinstall must not go on.
	var undo []func()
	defer func() {
		if err != nil {
			for i := len(undo) - 1; i >= 0; i-- {
				undo[i]()
			}
		}
		os.RemoveAll(trash)
	}()
	move := func(from, to string) error {
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		if err := os.Rename(from, to); err != nil {
			return err
		}
		undo = append(undo, func() { os.Rename(to, from) })
		return nil
	}
	for _, e := range top {
		name := e.Name()
		old := filepath.Join(home, name)
		if _, serr := os.Lstat(old); serr == nil {
			if err := move(old, filepath.Join(trash, name)); err != nil {
				return fmt.Errorf("replace %s (is a talk still open?): %w", name, err)
			}
		}
	}
	// A deck being shown lives in runtimes/<major>/work.
	if oldRt := filepath.Join(trash, "runtimes"); dirExists(oldRt) {
		majors, _ := os.ReadDir(oldRt)
		for _, m := range majors {
			work := filepath.Join(oldRt, m.Name(), "work")
			if dirExists(work) && dirExists(filepath.Join(staging, "runtimes", m.Name())) {
				if err := move(work, filepath.Join(staging, "runtimes", m.Name(), "work")); err != nil {
					return err
				}
			}
		}
	}
	for _, e := range top {
		if err := move(filepath.Join(staging, e.Name()), filepath.Join(home, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// unpack writes every file of src below dst, streaming each one.
func unpack(src fs.FS, dst string) error {
	buf := make([]byte, 256<<10)
	return fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return os.MkdirAll(dst, 0o755)
		}
		if !fs.ValidPath(p) || strings.ContainsAny(p, `\:`) {
			return fmt.Errorf("unsafe name in the bundled files: %q", p)
		}
		target := filepath.Join(dst, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("the bundled files hold something that is not a file: %s", p)
		}
		in, err := src.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		return writeFile(target, in, buf)
	})
}

func writeFile(target string, r io.Reader, buf []byte) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.CopyBuffer(f, struct{ io.Reader }{r}, buf); err != nil {
		f.Close()
		return fmt.Errorf("write %s: %w", target, err)
	}
	return f.Close()
}

// copyProgram copies self to dst. A running program can be renamed but not
// overwritten, so an existing dst is moved aside first.
func copyProgram(self, dst string) error {
	if a, err := os.Stat(self); err == nil {
		if b, err := os.Stat(dst); err == nil && os.SameFile(a, b) {
			return nil // already running from the install folder
		}
	}
	in, err := os.Open(self)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, old := dst+".new", dst+".old"
	if err := writeFile(tmp, in, make([]byte, 256<<10)); err != nil {
		return err
	}
	os.Remove(old)
	if _, err := os.Lstat(dst); err == nil {
		if err := os.Rename(dst, old); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Rename(old, dst)
		os.Remove(tmp)
		return err
	}
	os.Remove(old) // fails while the old program still runs; the next install clears it
	return nil
}

// Uninstall removes the file registration and what Install put in home, which must be an
// install folder (ErrNotInstalled otherwise, with nothing touched). The journal stays when
// it holds anything: it may hold notes that are in no deck file. Files in home that are not
// ours stay too. The running program cannot delete itself: see ScheduleCleanup.
func Uninstall(home string) error {
	self, err := os.Executable()
	if err != nil {
		self = ""
	}
	_, err = uninstall(home, self, platformRegistry)
	return err
}

// ScheduleCleanup, to be called just before the process exits after Uninstall, deletes the
// running program, and then home if it is empty, once the program has ended. It does
// nothing when the program is not in home.
func ScheduleCleanup(home string) {
	self, err := os.Executable()
	if err != nil {
		return
	}
	a, err1 := os.Stat(filepath.Dir(self))
	b, err2 := os.Stat(home)
	if err1 != nil || err2 != nil || !os.SameFile(a, b) {
		return
	}
	if _, err := os.Stat(self); err == nil {
		scheduleSelfDelete(self, home)
	}
}

// Environment variables that carry the paths to the cleanup command, and its command line.
// It holds no path: cmd.exe expands the variables inside quotes, so nothing a folder name
// holds is parsed as command syntax. It tries about 60 s: the program's file stays locked
// until the process has ended, and a notice may be on screen until then.
const (
	cleanupExeVar  = "CLEANUP_EXE"
	cleanupHomeVar = "CLEANUP_HOME"
	cleanupLine    = `/d /s /c "for /l %i in (1,1,60) do (del /f /q "%` + cleanupExeVar + `%" >nul 2>&1 & ` +
		`if exist "%` + cleanupExeVar + `%" (ping -n 2 127.0.0.1 >nul) else (rmdir "%` + cleanupHomeVar + `%" >nul 2>&1 & exit))"`
)

// ownedEntries are the names in home that Install and the viewer create.
var ownedEntries = []string{"node", "runtimes", "instances", "logs", trust.FileName}

// uninstall reports left when the running program (self) is in home and so
// could not be deleted.
func uninstall(home, self string, reg assoc) (left bool, err error) {
	home, err = filepath.Abs(home)
	if err != nil {
		return false, err
	}
	if !IsInstalled(home) {
		return false, fmt.Errorf("%w: %s", ErrNotInstalled, home)
	}
	// Move node/ out of the way first: it cannot be moved while a talk runs, and then
	// nothing has been changed, in the registry or on disk.
	trash := filepath.Join(home, fmt.Sprintf("%s-%d", trashName, time.Now().UnixNano()))
	if dirExists(filepath.Join(home, "node")) {
		if err := os.MkdirAll(trash, 0o755); err != nil {
			return false, err
		}
		if err := os.Rename(filepath.Join(home, "node"), filepath.Join(trash, "node")); err != nil {
			os.Remove(trash)
			return false, fmt.Errorf("cannot remove %s (is a talk still open?): %w", "node", err)
		}
	}
	rerr := reg.remove()
	defer reg.notify()
	errs := []error{rerr}
	rm := func(p string) {
		if err := os.RemoveAll(p); err != nil {
			errs = append(errs, err)
		}
	}
	rm(trash)
	// Leftovers of earlier installs may still be locked; they are no reason to stop.
	stale, _ := filepath.Glob(filepath.Join(home, ".install-*"))
	for _, p := range stale {
		os.RemoveAll(p)
	}
	for _, n := range ownedEntries {
		rm(filepath.Join(home, n))
	}
	if j := filepath.Join(home, journalName); emptyDir(j) {
		rm(j)
	}
	// The program and the marker last: while they stay, an uninstall that failed can be run again.
	if errors.Join(errs...) == nil {
		var selfInfo fs.FileInfo
		if self != "" {
			selfInfo, _ = os.Stat(self)
		}
		for _, n := range []string{ExeName(), ExeName() + ".new", ExeName() + ".old"} {
			p := filepath.Join(home, n)
			if fi, err := os.Stat(p); err == nil && selfInfo != nil && os.SameFile(fi, selfInfo) {
				left = true
				continue
			}
			rm(p)
		}
		if errors.Join(errs...) == nil {
			rm(filepath.Join(home, markerName()))
		}
	}
	os.Remove(home) // only succeeds when empty
	return left, errors.Join(errs...)
}

// KeptJournal says whether home still holds a journal with something in it.
func KeptJournal(home string) bool {
	p := filepath.Join(home, journalName)
	return dirExists(p) && !emptyDir(p)
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func emptyDir(p string) bool {
	ents, err := os.ReadDir(p)
	return err == nil && len(ents) == 0
}
