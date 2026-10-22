package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"stagepack/internal/app"
	"stagepack/internal/brand"
	"stagepack/internal/deck"
	"stagepack/internal/install"
	"stagepack/internal/ui"
)

func TestPackCommand(t *testing.T) {
	out := filepath.Join(t.TempDir(), "t"+brand.Ext)
	var so, se bytes.Buffer
	// DIR first, flags after, repeated flags
	code := run([]string{"pack", "../../testdata/deck", "-o", out, "--title", "T", "--meta", "a=1", "--meta", "b=x=y", "--exclude", "public/nothing"}, &so, &se)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, se.String())
	}
	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	m, err := deck.ReadManifest(&zr.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "T" || m.Meta["a"] != "1" || m.Meta["b"] != "x=y" {
		t.Errorf("manifest = %+v", m)
	}
}

func TestPackCommandUsage(t *testing.T) {
	for _, args := range [][]string{{"pack"}, {"pack", "dir"}, {"pack", "-o", "x"}, {"pack", "a", "b", "-o", "x"}, {"pack", "d", "-o", "x", "--meta", "nokey"}} {
		var so, se bytes.Buffer
		if code := run(args, &so, &se); code != 2 {
			t.Errorf("%v: exit %d, want 2 (%s)", args, code, se.String())
		}
	}
	var so, se bytes.Buffer
	if code := run([]string{"pack", "/nonexistent-dir", "-o", filepath.Join(os.TempDir(), "x")}, &so, &se); code != 1 || !strings.Contains(se.String(), "not a folder") {
		t.Errorf("missing dir: exit %d, %q", code, se.String())
	}
}

// fakeOpen replaces app.Open for one test and records its options.
func fakeOpen(t *testing.T, ret error) *[]app.Options {
	t.Helper()
	var got []app.Options
	old := openDeck
	openDeck = func(ctx context.Context, o app.Options) error { got = append(got, o); return ret }
	t.Cleanup(func() { openDeck = old })
	return &got
}

func TestOpenCommand(t *testing.T) {
	got := fakeOpen(t, nil)
	var so, se bytes.Buffer
	if code := run([]string{"open", "--headless", "--home", "/h", "x" + brand.Ext}, &so, &se); code != 0 {
		t.Fatalf("exit %d: %s", code, se.String())
	}
	if len(*got) != 1 || (*got)[0].Home != "/h" || (*got)[0].DeckPath != "x"+brand.Ext {
		t.Fatalf("Open got %+v", *got)
	}
	// headless: the UI writes JSON lines to stdout
	u := (*got)[0].UI(ui.Actions{})
	u.Notify("t", "m")
	if !strings.Contains(so.String(), `"event":"notify"`) {
		t.Errorf("headless UI wrote %q", so.String())
	}

	// FILE first, default home is the program's folder
	got2 := fakeOpen(t, errors.New("boom"))
	if code := run([]string{"open", "y" + brand.Ext, "--headless"}, &so, &se); code != 1 {
		t.Errorf("failed open: exit %d, want 1", code)
	}
	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	if len(*got2) != 1 || (*got2)[0].Home != filepath.Dir(exe) {
		t.Errorf("default home: %+v", *got2)
	}

	if (*got)[0].TrustCode || (*got2)[0].TrustCode {
		t.Error("TrustCode set without --trust-code")
	}

	// --trust-code; a talk not allowed to run its code says how to allow it
	got3 := fakeOpen(t, app.ErrNotTrusted)
	se.Reset()
	if code := run([]string{"open", "--trust-code", "z" + brand.Ext}, &so, &se); code != 1 {
		t.Errorf("not trusted: exit %d, want 1", code)
	}
	if len(*got3) != 1 || !(*got3)[0].TrustCode {
		t.Errorf("--trust-code: %+v", *got3)
	}
	if !strings.Contains(se.String(), "--trust-code") {
		t.Errorf("no hint about --trust-code: %q", se.String())
	}

	for _, args := range [][]string{{"open"}, {"open", "a", "b"}, {"open", "--nope", "a"}} {
		if code := run(args, &so, &se); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

func TestDoubleClickOnWindowsMeansOpen(t *testing.T) {
	old := goos
	t.Cleanup(func() { goos = old })
	got := fakeOpen(t, nil)
	var so, se bytes.Buffer
	deck := `C:\Talks\A` + strings.ToUpper(brand.Ext)

	goos = "linux"
	if code := run([]string{deck}, &so, &se); code != 2 || len(*got) != 0 {
		t.Errorf("linux: exit %d, opened %d", code, len(*got))
	}
	goos = "windows"
	if code := run([]string{deck}, &so, &se); code != 0 || len(*got) != 1 || (*got)[0].DeckPath != deck {
		t.Errorf("windows: exit %d, opened %+v (%s)", code, *got, se.String())
	}
}

// fakeInstaller replaces the install seams for one test and records what happened.
type fakeInstaller struct {
	calls     []string // "confirm: …", "busy: …", "install <home>", "uninstall <home>", "notice: …", "alert: …"
	yes       bool
	hasLoad   bool
	installed error
	kept      bool
	selfHome  string // the folder the running program is installed in, if any
}

func withFakeInstaller(t *testing.T, f *fakeInstaller) {
	t.Helper()
	old, oldGOOS := inst, goos
	t.Cleanup(func() { inst, goos = old, oldGOOS })
	goos = "windows"
	inst = installer{
		confirm: func(title, msg string) bool { f.calls = append(f.calls, "confirm: "+msg); return f.yes },
		notice:  func(title, msg string) { f.calls = append(f.calls, "notice: "+msg) },
		alert:   func(title, msg string) { f.calls = append(f.calls, "alert: "+msg) },
		busy: func(title, msg string) func() {
			f.calls = append(f.calls, "busy: "+msg)
			return func() { f.calls = append(f.calls, "busy done") }
		},
		payload: func(string) (fs.FS, bool) { return fstest.MapFS{}, f.hasLoad },
		install: func(home string, _ fs.FS) error { f.calls = append(f.calls, "install "+home); return f.installed },
		uninstall: func(home string) error {
			f.calls = append(f.calls, "uninstall "+home)
			return f.installed
		},
		keptJournal:   func(string) bool { return f.kept },
		cleanup:       func(home string) { f.calls = append(f.calls, "cleanup "+home) },
		installedHome: func() (string, bool) { return f.selfHome, f.selfHome != "" },
	}
}

func TestNoArgsOnWindowsInstalls(t *testing.T) {
	t.Setenv("LOCALAPPDATA", filepath.Join(t.TempDir(), "Local"))
	home := filepath.Join(os.Getenv("LOCALAPPDATA"), brand.Name)
	f := &fakeInstaller{yes: true, hasLoad: true}
	withFakeInstaller(t, f)
	var so, se bytes.Buffer
	if code := run(nil, &so, &se); code != 0 {
		t.Fatalf("exit %d: %s", code, se.String())
	}
	want := []string{
		"confirm: Install " + brand.Name + " for this user? It opens " + brand.Ext + " talks.",
		"busy: Installing " + brand.Name + "…",
		"install " + home,
		"busy done",
		"notice: Done. Double-click any " + brand.Ext + " file to present it.",
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("calls =\n%q\nwant\n%q", f.calls, want)
	}
}

func TestInstallDeclinedDoesNothing(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	f := &fakeInstaller{yes: false, hasLoad: true}
	withFakeInstaller(t, f)
	var so, se bytes.Buffer
	if code := run([]string{"install"}, &so, &se); code != 0 {
		t.Errorf("exit %d", code)
	}
	if len(f.calls) != 1 || !strings.HasPrefix(f.calls[0], "confirm:") {
		t.Errorf("calls = %q", f.calls)
	}
}

func TestInstallYesAndHomeFlags(t *testing.T) {
	f := &fakeInstaller{hasLoad: true} // yes is false: --yes must not ask
	withFakeInstaller(t, f)
	var so, se bytes.Buffer
	if code := run([]string{"install", "--yes", "--home", "/custom"}, &so, &se); code != 0 {
		t.Fatalf("exit %d: %s", code, se.String())
	}
	if !slices.Contains(f.calls, "install /custom") {
		t.Errorf("calls = %q", f.calls)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "confirm:") {
			t.Errorf("asked despite --yes: %q", c)
		}
	}
}

func TestInstallWithoutPayloadOrWithErrorAlerts(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	var so, se bytes.Buffer

	f := &fakeInstaller{yes: true, hasLoad: false}
	withFakeInstaller(t, f)
	if code := run([]string{"install"}, &so, &se); code != 1 {
		t.Errorf("no payload: exit %d", code)
	}
	if last := f.calls[len(f.calls)-1]; !strings.HasPrefix(last, "alert:") || slices.Contains(f.calls, "install "+filepath.Join(os.Getenv("LOCALAPPDATA"), brand.Name)) {
		t.Errorf("no payload: calls = %q", f.calls)
	}

	f = &fakeInstaller{yes: true, hasLoad: true, installed: errors.New("disk full")}
	withFakeInstaller(t, f)
	if code := run([]string{"install"}, &so, &se); code != 1 {
		t.Errorf("failed install: exit %d", code)
	}
	n := len(f.calls)
	if f.calls[n-2] != "busy done" || !strings.HasPrefix(f.calls[n-1], "alert:") || !strings.Contains(f.calls[n-1], "disk full") {
		t.Errorf("failed install: calls = %q", f.calls)
	}
}

func TestInstallIsForWindowsAndLinux(t *testing.T) {
	f := &fakeInstaller{yes: true, hasLoad: true}
	withFakeInstaller(t, f)
	goos = "darwin"
	var so, se bytes.Buffer
	for _, c := range []string{"install", "uninstall"} {
		if code := run([]string{c}, &so, &se); code != 2 {
			t.Errorf("%s on darwin: exit %d", c, code)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("calls on darwin: %q", f.calls)
	}
}

func TestInstallOnLinuxGoesToTheDataFolder(t *testing.T) {
	data := filepath.Join(t.TempDir(), "share")
	t.Setenv("XDG_DATA_HOME", data)
	f := &fakeInstaller{yes: true, hasLoad: true}
	withFakeInstaller(t, f)
	goos = "linux"
	home := filepath.Join(data, brand.AppID)
	var so, se bytes.Buffer
	if code := run([]string{"install"}, &so, &se); code != 0 {
		t.Fatalf("exit %d: %s", code, se.String())
	}
	if !slices.Contains(f.calls, "install "+home) {
		t.Errorf("calls = %q, want an install into %s", f.calls, home)
	}
	if last := f.calls[len(f.calls)-1]; !strings.Contains(last, filepath.Join(home, brand.AppID)+" uninstall") {
		t.Errorf("the notice does not say how to uninstall: %q", last)
	}
	f.calls = nil
	if code := run([]string{"uninstall"}, &so, &se); code != 0 || !slices.Contains(f.calls, "uninstall "+home) {
		t.Errorf("uninstall: exit %d, calls %q", code, f.calls)
	}

	// Without node/ and runtimes/ beside the program there is nothing to install.
	f.calls, f.hasLoad = nil, false
	if code := run([]string{"install", "--yes"}, &so, &se); code != 1 || !strings.Contains(strings.Join(f.calls, ""), "make build-linux") {
		t.Errorf("no runtime: exit %d, calls %q", code, f.calls)
	}
}

func TestTerminalDialogs(t *testing.T) {
	for in, want := range map[string]bool{"y\n": true, "Yes\n": true, " yes ": true, "n\n": false, "\n": false, "": false, "sure\n": false} {
		var out bytes.Buffer
		tm := terminal{in: strings.NewReader(in), out: &out, err: &out}
		if got := tm.confirm(brand.Name, "Install?"); got != want {
			t.Errorf("answer %q: %v, want %v", in, got, want)
		}
		if !strings.HasPrefix(out.String(), "Install? [y/N] ") {
			t.Errorf("prompt %q", out.String())
		}
		if in == "" && !strings.Contains(out.String(), "--yes") {
			t.Errorf("no answer, and no hint about --yes: %q", out.String())
		}
	}
	var out, errs bytes.Buffer
	tm := terminal{in: strings.NewReader(""), out: &out, err: &errs}
	tm.busy(brand.Name, "Installing…")()
	tm.notice(brand.Name, "Done.")
	tm.alert(brand.Name, "Broken.")
	if out.String() != "Installing…\nDone.\n" || errs.String() != brand.Name+": Broken.\n" {
		t.Errorf("out %q, err %q", out.String(), errs.String())
	}
	if l := defaultInstaller("linux"); l.payload == nil || l.confirm == nil {
		t.Error("no Linux installer")
	}
}

func TestUninstallFlow(t *testing.T) {
	t.Setenv("LOCALAPPDATA", filepath.Join(t.TempDir(), "Local"))
	home := filepath.Join(os.Getenv("LOCALAPPDATA"), brand.Name)
	f := &fakeInstaller{yes: true}
	withFakeInstaller(t, f)
	var so, se bytes.Buffer
	if code := run([]string{"uninstall"}, &so, &se); code != 0 {
		t.Fatalf("exit %d: %s", code, se.String())
	}
	// the confirm names the folder; the program's self-delete is scheduled after the notice
	want := []string{
		"confirm: Remove " + brand.Name + " from " + home + "? Your " + brand.Ext + " talks are not touched.",
		"uninstall " + home,
		"notice: " + brand.Name + " is removed. Your " + brand.Ext + " talks were not touched.",
		"cleanup " + home,
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("calls =\n%q\nwant\n%q", f.calls, want)
	}

	f = &fakeInstaller{yes: true, kept: true}
	withFakeInstaller(t, f)
	if code := run([]string{"uninstall"}, &so, &se); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if n := f.calls[len(f.calls)-2]; !strings.Contains(n, filepath.Join(home, "journal")) {
		t.Errorf("kept-journal notice does not say where: %q", n)
	}

	f = &fakeInstaller{yes: false}
	withFakeInstaller(t, f)
	run([]string{"uninstall"}, &so, &se)
	if len(f.calls) != 1 {
		t.Errorf("declined uninstall: %q", f.calls)
	}
}

func TestUninstallFromTheInstalledProgramUsesItsOwnFolder(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	f := &fakeInstaller{yes: true, selfHome: filepath.Join(t.TempDir(), "custom")}
	withFakeInstaller(t, f)
	var so, se bytes.Buffer
	if code := run([]string{"uninstall"}, &so, &se); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !slices.Contains(f.calls, "uninstall "+f.selfHome) {
		t.Errorf("calls = %q", f.calls)
	}
	// --home still wins
	f.calls = nil
	other := filepath.Join(t.TempDir(), "other")
	if code := run([]string{"uninstall", "--home", other}, &so, &se); code != 0 || !slices.Contains(f.calls, "uninstall "+other) {
		t.Errorf("--home: exit %d, calls = %q", code, f.calls)
	}
}

func TestUninstallRefusalAndErrorsAlertWithoutCleanup(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	var so, se bytes.Buffer
	for _, e := range []error{install.ErrNotInstalled, errors.New("locked")} {
		f := &fakeInstaller{yes: true, installed: e}
		withFakeInstaller(t, f)
		if code := run([]string{"uninstall"}, &so, &se); code != 1 {
			t.Errorf("%v: exit %d", e, code)
		}
		last := f.calls[len(f.calls)-1]
		if !strings.HasPrefix(last, "alert:") || strings.Contains(strings.Join(f.calls, "|"), "cleanup") {
			t.Errorf("%v: calls = %q", e, f.calls)
		}
	}
}

func TestRelativeHomeIsMadeAbsolute(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	var so, se bytes.Buffer
	for _, c := range []string{"install", "uninstall"} {
		f := &fakeInstaller{hasLoad: true}
		withFakeInstaller(t, f)
		if code := run([]string{c, "--yes", "--home", "rel"}, &so, &se); code != 0 {
			t.Fatalf("%s: exit %d: %s", c, code, se.String())
		}
		if !slices.Contains(f.calls, c+" "+filepath.Join(base, "rel")) {
			t.Errorf("%s: calls = %q", c, f.calls)
		}
	}
}
