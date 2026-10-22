package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"stagepack/internal/brand"
	"stagepack/internal/install"
	"stagepack/internal/ui"
)

// installer is everything install and uninstall do to the outside; tests replace it.
type installer struct {
	confirm func(title, msg string) bool
	notice  func(title, msg string)
	alert   func(title, msg string)
	busy    func(title, msg string) (done func())
	payload func(home string) (fs.FS, bool) // what to install into home

	install     func(home string, payload fs.FS) error
	uninstall   func(home string) error
	keptJournal func(home string) bool
	// cleanup deletes the running program once it has ended; installedHome is the folder
	// the running program is installed in, if it is the installed one.
	cleanup       func(home string)
	installedHome func() (string, bool)
}

var inst = defaultInstaller(runtime.GOOS)

// defaultInstaller is the installer for os: on Windows message boxes and the runtime
// inside the program; on Linux the terminal, and the runtime beside the program.
func defaultInstaller(forOS string) installer {
	in := installerBase
	if forOS != "windows" {
		t := terminal{in: os.Stdin, out: os.Stdout, err: os.Stderr}
		in.confirm, in.notice, in.alert, in.busy = t.confirm, t.notice, t.alert, t.busy
		in.payload = install.ProgramDirPayload
	}
	return in
}

var installerBase = installer{
	confirm:     ui.Confirm,
	notice:      ui.Notice,
	alert:       ui.Alert,
	busy:        ui.Busy,
	payload:     func(string) (fs.FS, bool) { return install.Payload() },
	install:     install.Install,
	uninstall:   install.Uninstall,
	keptJournal: install.KeptJournal,
	cleanup:     install.ScheduleCleanup,
	installedHome: func() (string, bool) {
		exe, err := os.Executable()
		if err == nil {
			exe, err = filepath.EvalSymlinks(exe)
		}
		if err != nil {
			return "", false
		}
		dir := filepath.Dir(exe)
		return dir, install.IsInstalled(dir)
	},
}

// defaultInstallHome is %LOCALAPPDATA%\<Name> on Windows, $XDG_DATA_HOME/<AppID>
// (~/.local/share/<AppID>) elsewhere.
func defaultInstallHome() (string, error) {
	if goos != "windows" {
		data, err := install.XDGDataHome(os.Getenv)
		if err != nil {
			return "", err
		}
		return filepath.Join(data, brand.AppID), nil
	}
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		return "", errors.New("LOCALAPPDATA is not set")
	}
	return filepath.Join(base, brand.Name), nil
}

// defaultHomeHelp names the default install folder in the usage text.
func defaultHomeHelp() string {
	if goos != "windows" {
		return "$XDG_DATA_HOME/" + brand.AppID + ", ~/.local/share/" + brand.AppID
	}
	return "%LOCALAPPDATA%\\" + brand.Name
}

// resolveHome is the install folder, as an absolute path: the --home flag, else (for an
// uninstall run by the installed program) the folder that program is in, else
// the default (defaultInstallHome).
func resolveHome(flagHome string, own bool) (string, error) {
	home := flagHome
	if home == "" && own {
		if h, ok := inst.installedHome(); ok {
			home = h
		}
	}
	if home == "" {
		var err error
		if home, err = defaultInstallHome(); err != nil {
			return "", err
		}
	}
	return filepath.Abs(home)
}

// installFlags parses --yes and --home, which install and uninstall share.
func installFlags(name string, args []string, stderr io.Writer) (home string, yes bool, ok bool) {
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(stderr)
	fl.BoolVar(&yes, "yes", false, "do not ask first")
	fl.StringVar(&home, "home", "", "the install folder (default: "+defaultHomeHelp()+")")
	if err := fl.Parse(args); err != nil || fl.NArg() != 0 {
		if err == nil {
			fmt.Fprintf(stderr, "usage: %s %s [--yes] [--home DIR]\n", cmdName, name)
		}
		return "", false, false
	}
	return home, yes, true
}

// runInstall asks, unpacks the bundled runtime, registers the file type and
// says so. On Windows everything the person sees is a message box: this runs
// without a console. On Linux it runs in a terminal.
func runInstall(args []string, stderr io.Writer) int {
	home, yes, ok := installFlags("install", args, stderr)
	if !ok {
		return 2
	}
	home, err := resolveHome(home, false)
	if err != nil {
		inst.alert(brand.Name, "Cannot install: "+err.Error())
		return 1
	}
	defer startLog(home)()
	payload, has := inst.payload(home)
	if !has {
		if goos == "windows" {
			inst.alert(brand.Name, "This copy of "+brand.Name+" has no runtime bundled with it, so it cannot install itself.")
		} else {
			inst.alert(brand.Name, "This copy of "+brand.Name+" has no node/ and runtimes/ folder beside it, so it cannot install itself. "+
				"Run the one in dist/linux (make build-linux).")
		}
		return 1
	}
	if !yes && !inst.confirm(brand.Name, "Install "+brand.Name+" for this user? It opens "+brand.Ext+" talks.") {
		return 0
	}
	done := inst.busy(brand.Name, "Installing "+brand.Name+"…")
	err = inst.install(home, payload)
	done()
	if err != nil {
		inst.alert(brand.Name, "Could not install "+brand.Name+": "+err.Error())
		return 1
	}
	msg := "Done. Double-click any " + brand.Ext + " file to present it."
	if goos != "windows" {
		msg += "\nInstalled in " + home + ". To remove it: " + filepath.Join(home, install.ExeName()) + " uninstall"
	}
	inst.notice(brand.Name, msg)
	return 0
}

// runUninstall removes the file type and the install folder, keeping notes
// that are not in any deck yet.
func runUninstall(args []string, stderr io.Writer) int {
	home, yes, ok := installFlags("uninstall", args, stderr)
	if !ok {
		return 2
	}
	home, err := resolveHome(home, true)
	if err != nil {
		inst.alert(brand.Name, "Cannot uninstall: "+err.Error())
		return 1
	}
	stopLog := sync.OnceFunc(startLog(home))
	defer stopLog()
	if !yes && !inst.confirm(brand.Name, "Remove "+brand.Name+" from "+home+"? Your "+brand.Ext+" talks are not touched.") {
		return 0
	}
	stopLog() // the uninstall deletes logs/, which Windows refuses while the file is open
	if err := inst.uninstall(home); err != nil {
		if errors.Is(err, install.ErrNotInstalled) {
			inst.alert(brand.Name, brand.Name+" is not installed in "+home+". Nothing was removed.")
		} else {
			inst.alert(brand.Name, "Could not remove "+brand.Name+" completely: "+err.Error())
		}
		return 1
	}
	msg := brand.Name + " is removed. Your " + brand.Ext + " talks were not touched."
	if inst.keptJournal(home) {
		msg += "\n\nNotes that were not yet saved in a talk are kept in " + filepath.Join(home, "journal") +
			". Delete that folder when you no longer need them."
	}
	inst.notice(brand.Name, msg)
	// Only now: the program's file is locked until this process ends, and the notice is modal.
	inst.cleanup(home)
	return 0
}

// terminal asks and tells on a terminal: install and uninstall on Linux are run from a shell.
type terminal struct {
	in       io.Reader
	out, err io.Writer
}

// confirm asks msg and reads the answer; only y or yes is yes, and no answer is no.
func (t terminal) confirm(title, msg string) bool {
	fmt.Fprintf(t.out, "%s [y/N] ", msg)
	line, err := bufio.NewReader(t.in).ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(t.out, "\n(no answer, so no: pass --yes to skip the question)")
		return false
	}
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes"
}

func (t terminal) notice(title, msg string) { fmt.Fprintln(t.out, msg) }

func (t terminal) alert(title, msg string) { fmt.Fprintln(t.err, title+": "+msg) }

func (t terminal) busy(title, msg string) (done func()) {
	fmt.Fprintln(t.out, msg)
	return func() {}
}
