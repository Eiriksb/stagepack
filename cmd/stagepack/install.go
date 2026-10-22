package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"stagepack/internal/brand"
	"stagepack/internal/install"
	"stagepack/internal/ui"
)

// installer is everything install and uninstall do to the outside; tests replace it.
type installer struct {
	confirm     func(title, msg string) bool
	notice      func(title, msg string)
	alert       func(title, msg string)
	busy        func(title, msg string) (done func())
	payload     func() (fs.FS, bool)
	install     func(home string, payload fs.FS) error
	uninstall   func(home string) error
	keptJournal func(home string) bool
	// cleanup deletes the running program once it has ended; installedHome is the folder
	// the running program is installed in, if it is the installed one.
	cleanup       func(home string)
	installedHome func() (string, bool)
}

var inst = installer{
	confirm:     ui.Confirm,
	notice:      ui.Notice,
	alert:       ui.Alert,
	busy:        ui.Busy,
	payload:     install.Payload,
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

// defaultInstallHome is %LOCALAPPDATA%\<Name>.
func defaultInstallHome() (string, error) {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		return "", errors.New("LOCALAPPDATA is not set")
	}
	return filepath.Join(base, brand.Name), nil
}

// resolveHome is the install folder, as an absolute path: the --home flag, else (for an
// uninstall run by the installed program) the folder that program is in, else
// %LOCALAPPDATA%\<Name>.
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
	fl.StringVar(&home, "home", "", "the install folder (default: %LOCALAPPDATA%\\"+brand.Name+")")
	if err := fl.Parse(args); err != nil || fl.NArg() != 0 {
		if err == nil {
			fmt.Fprintf(stderr, "usage: %s %s [--yes] [--home DIR]\n", cmdName, name)
		}
		return "", false, false
	}
	return home, yes, true
}

// runInstall asks, unpacks the bundled runtime, registers the file type and
// says so. Everything the person sees is a message box: this runs without a console.
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
	payload, has := inst.payload()
	if !has {
		inst.alert(brand.Name, "This copy of "+brand.Name+" has no runtime bundled with it, so it cannot install itself.")
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
	inst.notice(brand.Name, "Done. Double-click any "+brand.Ext+" file to present it.")
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
