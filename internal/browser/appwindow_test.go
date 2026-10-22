//go:build !windows

package browser

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeBrowser writes a script that logs its arguments and, unless told to end
// at once, runs until it gets SIGTERM, as a browser that owns its profile does.
func fakeBrowser(t *testing.T, mode string) (exe, argLog string) {
	t.Helper()
	dir := t.TempDir()
	argLog = filepath.Join(dir, "args")
	exe = filepath.Join(dir, "chrome")
	body := "#!/bin/sh\necho \"$@\" >> '" + argLog + "'\n"
	switch mode {
	case "run":
		body += "trap 'exit 0' TERM\nwhile :; do sleep 0.05; done\n"
	case "fail":
		body += "exit 3\n"
	case "handoff":
		body += "exit 0\n"
	}
	if err := os.WriteFile(exe, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe, argLog
}

func lines(t *testing.T, p string) []string {
	t.Helper()
	data, _ := os.ReadFile(p)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestAppArgs(t *testing.T) {
	a := AppArgs("http://127.0.0.1:1/presenter/", "/p", "linux")
	for _, want := range []string{"--app=http://127.0.0.1:1/presenter/", "--user-data-dir=/p", "--no-first-run", "--password-store=basic"} {
		if !slices.Contains(a, want) {
			t.Errorf("args %q lack %q", a, want)
		}
	}
	if slices.Contains(AppArgs("u", `C:\p`, "windows"), "--password-store=basic") {
		t.Error("--password-store is for Linux")
	}
}

func TestAppWindowStopCloses(t *testing.T) {
	exe, args := fakeBrowser(t, "run")
	profile := filepath.Join(t.TempDir(), "browser", "k")
	w := NewAppWindow(exe, profile, "linux")
	w.handoff = 50 * time.Millisecond
	if err := w.Open("http://127.0.0.1:1/presenter/"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // past the handoff time: it shows the window itself
	if err := w.Open("http://127.0.0.1:1/"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "two launches", func() bool { return len(lines(t, args)) == 2 })
	l := lines(t, args)
	if !strings.Contains(l[0], "--app=http://127.0.0.1:1/presenter/ --user-data-dir="+profile) || !strings.Contains(l[1], "--app=http://127.0.0.1:1/ ") {
		t.Errorf("launches = %q", l)
	}
	w.Close()
	select {
	case <-w.Closed():
		t.Error("closing for Stop counted as the person closing the windows")
	default:
	}
	if _, err := os.Stat(profile); err == nil {
		t.Error("the profile is left behind")
	}
	if err := w.Open("http://x/"); err == nil {
		t.Error("a window opened after Close")
	}
}

func TestAppWindowClosedByThePerson(t *testing.T) {
	exe, _ := fakeBrowser(t, "run")
	w := NewAppWindow(exe, filepath.Join(t.TempDir(), "p"), "linux")
	w.handoff = 50 * time.Millisecond
	if err := w.Open("http://u/"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	w.mu.Lock()
	p := w.proc
	w.mu.Unlock()
	terminate(p) // as when the last window is closed
	select {
	case <-w.Closed():
	case <-time.After(5 * time.Second):
		t.Fatal("Closed not signalled")
	}
	w.Close() // nothing left to close
}

func TestAppWindowFailureFallsBack(t *testing.T) {
	for _, c := range []struct {
		mode     string
		fallback bool
	}{{"fail", true}, {"handoff", false}} {
		exe, _ := fakeBrowser(t, c.mode)
		w := NewAppWindow(exe, filepath.Join(t.TempDir(), "p"), "linux")
		got := make(chan string, 1)
		w.fallback = func(u string) error { got <- u; return nil }
		if err := w.Open("http://u/presenter/"); err != nil {
			t.Fatal(err)
		}
		select {
		case u := <-got:
			if !c.fallback || u != "http://u/presenter/" {
				t.Errorf("%s: default browser opened %q", c.mode, u)
			}
		case <-time.After(time.Second):
			if c.fallback {
				t.Errorf("%s: the default browser was not opened", c.mode)
			}
		}
		select {
		case <-w.Closed():
			t.Errorf("%s: a browser that ended at once counted as closed windows", c.mode)
		default:
		}
	}
}

func TestFindAppBrowser(t *testing.T) {
	dir := t.TempDir()
	chrome := filepath.Join(dir, "google-chrome")
	os.WriteFile(chrome, []byte("#!/bin/sh\n"), 0o755)
	look := func(have map[string]string) func(string) (string, error) {
		return func(n string) (string, error) {
			if p, ok := have[n]; ok {
				return p, nil
			}
			return "", errors.New("not found")
		}
	}
	if p, ok := FindAppBrowser("linux", os.Getenv, look(map[string]string{"google-chrome": chrome})); !ok || p != chrome {
		t.Errorf("found %q, %v", p, ok)
	}
	// A snap is passed over.
	snap := filepath.Join(dir, "chromium")
	os.Symlink("/usr/lib/snapd/snapd", snap)
	if p, ok := FindAppBrowser("linux", os.Getenv, look(map[string]string{"chromium": snap})); ok {
		t.Errorf("snap chromium chosen: %q", p)
	}
	if _, ok := FindAppBrowser("linux", os.Getenv, look(nil)); ok {
		t.Error("found a browser where there is none")
	}
}

func TestPruneProfiles(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"ours", "stale", "running"} {
		os.MkdirAll(filepath.Join(dir, n, "Default"), 0o755)
	}
	os.Symlink("host-123", filepath.Join(dir, "running", "SingletonLock"))
	pruneProfiles(dir, "ours")
	for n, want := range map[string]bool{"ours": true, "stale": false, "running": true} {
		if _, err := os.Stat(filepath.Join(dir, n)); (err == nil) != want {
			t.Errorf("%s kept = %v, want %v", n, err == nil, want)
		}
	}
}
