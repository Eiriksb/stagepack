//go:build !windows

package app

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"stagepack/internal/browser"
	"stagepack/internal/instance"
)

// useFakeChrome puts a google-chrome on PATH that logs its arguments and runs until SIGTERM,
// and returns its log and a function giving the PID of the browser it last started.
func useFakeChrome(t *testing.T) (argLog string, pid func() int) {
	t.Helper()
	dir := t.TempDir()
	argLog, pidFile := filepath.Join(dir, "args"), filepath.Join(dir, "pid")
	script := "#!/bin/sh\necho \"$@\" >> '" + argLog + "'\necho $$ > '" + pidFile + "'\ntrap 'exit 0' TERM\nwhile :; do sleep 0.05; done\n"
	if err := os.WriteFile(filepath.Join(dir, "google-chrome"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("BROWSER", "")
	old := browser.Handoff
	browser.Handoff = 100 * time.Millisecond
	t.Cleanup(func() { browser.Handoff = old })
	return argLog, func() int {
		data, _ := os.ReadFile(pidFile)
		n, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		return n
	}
}

func alive(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }

func TestAppWindowOpensAndStopClosesIt(t *testing.T) {
	useFakeSlidev(t)
	argLog, pid := useFakeChrome(t)
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir())
	fu := newFakeUI()
	res := openAsync(Options{Home: home, DeckPath: deckPath, UI: fu.factory, GOOS: "linux", Env: os.Getenv})
	fu.waitRunning(t)
	data, _ := os.ReadFile(argLog)
	profile := filepath.Join(home, "browser", instance.Key(deckPath))
	if !strings.Contains(string(data), "/presenter/ --user-data-dir="+profile) || !strings.HasPrefix(string(data), "--app=") {
		t.Errorf("browser started with %q", data)
	}
	p := pid()
	fu.acts.Stop()
	if err := waitResult(t, res); err != nil {
		t.Fatal(err)
	}
	if alive(p) {
		t.Error("the browser still runs after Stop")
	}
	if _, err := os.Stat(profile); err == nil {
		t.Error("the browser profile is left behind")
	}
}

func TestClosingTheWindowsStopsTheTalk(t *testing.T) {
	fs := useFakeSlidev(t)
	_, pid := useFakeChrome(t)
	home := t.TempDir()
	fu := newFakeUI()
	res := openAsync(Options{Home: home, DeckPath: packFixture(t, t.TempDir()), UI: fu.factory, GOOS: "linux", Env: os.Getenv})
	fu.waitRunning(t)
	time.Sleep(200 * time.Millisecond) // past Handoff: the browser shows the window itself
	syscall.Kill(pid(), syscall.SIGTERM)
	if err := waitResult(t, res); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fs.configs()[0].WorkDir); !os.IsNotExist(err) {
		t.Errorf("work folder left behind (%v)", err)
	}
}

func TestDefaultBrowserOption(t *testing.T) {
	useFakeSlidev(t)
	argLog, _ := useFakeChrome(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "xdg")
	os.WriteFile(filepath.Join(os.Getenv("PATH"), "xdg-open"), []byte("#!/bin/sh\necho \"$@\" > '"+marker+"'\n"), 0o755)
	fu := newFakeUI()
	res := openAsync(Options{Home: t.TempDir(), DeckPath: packFixture(t, dir), UI: fu.factory, GOOS: "linux", Env: os.Getenv, DefaultBrowser: true})
	fu.waitRunning(t)
	deadline := time.Now().Add(5 * time.Second)
	for _, err := os.Stat(marker); err != nil && time.Now().Before(deadline); _, err = os.Stat(marker) {
		time.Sleep(10 * time.Millisecond)
	}
	fu.acts.Stop()
	waitResult(t, res)
	if _, err := os.Stat(marker); err != nil {
		t.Error("the default browser was not opened")
	}
	if _, err := os.Stat(argLog); err == nil {
		t.Error("an app window was opened although the default browser was asked for")
	}
}
