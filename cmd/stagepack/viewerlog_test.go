package main

import (
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"stagepack/internal/brand"
)

// Tests never write the viewer log into a home they name: see TestStartViewerLog.
func TestMain(m *testing.M) {
	startLog = func(string) func() { return func() {} }
	os.Exit(m.Run())
}

func readLog(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestOpenLogFileRotates(t *testing.T) {
	p := filepath.Join(t.TempDir(), "logs", "viewer.log")
	write := func(s string, max int64) {
		t.Helper()
		f, err := openLogFile(p, max)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(s); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	write("one\n", 10) // the folder is made
	write("two\n", 10) // small enough: appended
	if got := readLog(t, p); got != "one\ntwo\n" {
		t.Fatalf("log = %q", got)
	}
	write("three, a long line\n", 10) // still appended: the size is checked at the start
	write("four\n", 10)               // now over 10 bytes: rotated first
	if got := readLog(t, p); got != "four\n" {
		t.Errorf("log after rotation = %q", got)
	}
	if got := readLog(t, p+".1"); got != "one\ntwo\nthree, a long line\n" {
		t.Errorf("log.1 = %q", got)
	}
	write("five, longer than ten\n", 10)
	write("six\n", 10) // rotated again: the old .1 is replaced
	if got := readLog(t, p+".1"); got != "four\nfive, longer than ten\n" {
		t.Errorf("log.1 after the second rotation = %q", got)
	}
	if got := readLog(t, p); got != "six\n" {
		t.Errorf("log = %q", got)
	}
}

func TestStartViewerLog(t *testing.T) {
	home := t.TempDir()
	stop := startViewerLog(home)
	log.Printf("hello from the test")
	stop()
	log.Printf("after stop") // goes to stderr again, not into the file
	got := readLog(t, filepath.Join(home, "logs", "viewer.log"))
	if !strings.Contains(got, "hello from the test") || strings.Contains(got, "after stop") {
		t.Errorf("viewer.log = %q", got)
	}
	// A home that can't hold a log: logging carries on to stderr.
	stop = startViewerLog(filepath.Join(home, "logs", "viewer.log", "not-a-folder"))
	log.Printf("still fine")
	stop()
}

// The uninstall removes logs/: the log file must be closed first (Windows can't delete an open file).
func TestInstallAndUninstallLog(t *testing.T) {
	f := &fakeInstaller{hasLoad: true, yes: true}
	withFakeInstaller(t, f)
	old := startLog
	t.Cleanup(func() { startLog = old })
	startLog = func(home string) func() {
		f.calls = append(f.calls, "log "+home)
		return func() { f.calls = append(f.calls, "log stopped") }
	}
	home := t.TempDir()
	var so, se strings.Builder
	if code := run([]string{"install", "--home", home}, &so, &se); code != 0 {
		t.Fatalf("install: exit %d: %s", code, se.String())
	}
	if i, j := slices.Index(f.calls, "log "+home), slices.Index(f.calls, "install "+home); i < 0 || i > j ||
		f.calls[len(f.calls)-1] != "log stopped" {
		t.Errorf("install calls = %q, want the log started before the install and stopped at the end", f.calls)
	}
	f.calls = nil
	if code := run([]string{"uninstall", "--home", home}, &so, &se); code != 0 {
		t.Fatalf("uninstall: exit %d: %s", code, se.String())
	}
	if i, j, k := slices.Index(f.calls, "log "+home), slices.Index(f.calls, "log stopped"), slices.Index(f.calls, "uninstall "+home); i < 0 || j < i || k < j {
		t.Errorf("uninstall calls = %q, want the log stopped before the uninstall", f.calls)
	}
}

func TestOpenStartsTheLog(t *testing.T) {
	fakeOpen(t, nil)
	old := startLog
	t.Cleanup(func() { startLog = old })
	var homes []string
	stopped := 0
	startLog = func(home string) func() { homes = append(homes, home); return func() { stopped++ } }
	var so, se strings.Builder
	if code := run([]string{"open", "--headless", "--home", "/h", "x" + brand.Ext}, &so, &se); code != 0 {
		t.Fatalf("exit %d: %s", code, se.String())
	}
	if !slices.Equal(homes, []string{"/h"}) || stopped != 1 {
		t.Errorf("log started for %q, stopped %d times", homes, stopped)
	}
}
