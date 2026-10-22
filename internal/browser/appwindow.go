package browser

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// AppWindow shows a talk in windows of its own: a Chromium-family browser
// (Chrome, Edge, Chromium, Brave) in app mode, so with no tabs and no address
// bar, and with a profile of its own, so that the windows belong to one browser
// process this program started. Stop can then close them, and closing them all
// is noticed (Closed).
type AppWindow struct {
	exe, profile string
	goos         string
	// handoff: a browser that ends sooner than this after it was started did not
	// show the window itself (another browser on the profile did, or it failed).
	handoff time.Duration
	// fallback opens a URL when the browser could not (default: Open).
	fallback func(url string) error

	mu      sync.Mutex
	proc    *os.Process   // the browser process, while it runs
	ended   chan struct{} // closed when proc has ended
	closing bool          // Close was called
	closed  chan struct{} // closed when the windows were closed by the person
	once    sync.Once
}

// Handoff is how long a browser must run to have shown the window itself: one
// that ends sooner gave the window to another browser on the same profile, or
// failed. Tests shorten it.
var Handoff = 5 * time.Second

// NewAppWindow returns app windows of the browser exe, with their profile in
// the folder profile (made when needed, and removed once the browser ends).
func NewAppWindow(exe, profile, goos string) *AppWindow {
	return &AppWindow{exe: exe, profile: profile, goos: goos, handoff: Handoff, fallback: Open, closed: make(chan struct{})}
}

// AppArgs is the browser's command line for url, after the program.
func AppArgs(url, profile, goos string) []string {
	args := []string{
		"--app=" + url,
		"--user-data-dir=" + profile,
		"--no-first-run",
		"--no-default-browser-check",
		"--hide-crash-restore-bubble",
		// Nothing the talk needs comes from the browser's own services: keep it offline and quiet.
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-sync",
		"--disable-features=Translate",
	}
	if goos != "windows" {
		args = append(args, "--password-store=basic") // no keyring prompt for a new profile
	}
	return args
}

// Open shows url in a new app window. The first window starts the browser;
// later ones go to it.
func (w *AppWindow) Open(url string) error {
	if err := os.MkdirAll(w.profile, 0o700); err != nil {
		return err
	}
	// Not hidden like Open's helper: on Windows that would hide the browser's first window.
	cmd := exec.Command(w.exe, AppArgs(url, w.profile, w.goos)...)
	ownGroup(cmd)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closing {
		return errors.New("the talk is closing")
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", filepath.Base(w.exe), err)
	}
	if w.proc != nil {
		go cmd.Wait() // it hands the URL to the running browser and ends
		return nil
	}
	pruneProfiles(filepath.Dir(w.profile), filepath.Base(w.profile))
	w.proc, w.ended = cmd.Process, make(chan struct{})
	go w.watch(cmd, url, time.Now(), w.ended)
	return nil
}

// watch waits for the browser to end. After a while: the person closed its
// windows. At once: it gave the window to another browser on the same
// profile, or it failed, and then the default browser opens url.
func (w *AppWindow) watch(cmd *exec.Cmd, url string, started time.Time, ended chan struct{}) {
	err := cmd.Wait()
	ranFor := time.Since(started)
	w.mu.Lock()
	w.proc = nil
	closing := w.closing
	w.mu.Unlock()
	if ranFor < w.handoff && !closing {
		if err != nil {
			log.Printf("%s ended at once (%v): opening the default browser instead", filepath.Base(w.exe), err)
			if ferr := w.fallback(url); ferr != nil {
				log.Printf("open the default browser: %v", ferr)
			}
		} else {
			log.Printf("%s ended at once: another browser on %s shows the window", filepath.Base(w.exe), w.profile)
		}
		close(ended)
		return
	}
	groupGone(cmd.Process.Pid, 3*time.Second)
	if err := os.RemoveAll(w.profile); err != nil {
		log.Printf("remove the browser profile: %v", err)
	}
	close(ended)
	if !closing {
		w.once.Do(func() { close(w.closed) })
	}
}

// pruneProfiles deletes the profiles in dir, other than keep, that no browser
// uses (left by a crash, or by helpers that outlived groupGone). A browser that
// runs holds SingletonLock (Linux) or lockfile (Windows, which also refuses
// to delete it).
func pruneProfiles(dir, keep string) {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.Name() == keep || !e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if _, err := os.Lstat(filepath.Join(p, "SingletonLock")); err == nil {
			continue
		}
		if err := os.Remove(filepath.Join(p, "lockfile")); err != nil && !errors.Is(err, os.ErrNotExist) {
			continue
		}
		os.RemoveAll(p)
	}
}

// Closed is closed when the person has closed all the windows.
func (w *AppWindow) Closed() <-chan struct{} { return w.closed }

// Close closes the windows and waits (a few seconds at most) for the browser to end.
func (w *AppWindow) Close() {
	w.mu.Lock()
	w.closing = true
	p, ended := w.proc, w.ended
	w.mu.Unlock()
	if p == nil {
		return
	}
	terminate(p)
	select {
	case <-ended:
		return
	case <-time.After(5 * time.Second):
	}
	p.Kill()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		log.Printf("%s did not end", filepath.Base(w.exe))
	}
}

// FindAppBrowser looks for a Chromium-family browser that can show app windows.
// A snap package is passed over: its confinement keeps it out of the profile folder.
func FindAppBrowser(goos string, getenv func(string) string, lookPath func(string) (string, error)) (string, bool) {
	if goos == "windows" {
		var bases []string
		for _, v := range []string{"PROGRAMFILES", "PROGRAMFILES(X86)", "LOCALAPPDATA"} {
			if b := getenv(v); b != "" {
				bases = append(bases, b)
			}
		}
		for _, rel := range []string{
			`Google\Chrome\Application\chrome.exe`,
			`Microsoft\Edge\Application\msedge.exe`,
			`BraveSoftware\Brave-Browser\Application\brave.exe`,
			`Chromium\Application\chrome.exe`,
		} {
			for _, b := range bases {
				p := b + `\` + rel
				if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
					return p, true
				}
			}
		}
		return "", false
	}
	for _, name := range []string{
		"google-chrome-stable", "google-chrome", "chromium", "chromium-browser",
		"microsoft-edge-stable", "microsoft-edge", "brave-browser",
	} {
		p, err := lookPath(name)
		if err != nil {
			continue
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil || strings.HasPrefix(real, "/snap/") || strings.HasSuffix(real, "/snapd") || strings.HasPrefix(p, "/snap/") {
			continue
		}
		return p, true
	}
	return "", false
}
