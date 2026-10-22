package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"stagepack/internal/brand"
	"stagepack/internal/deck"
	"stagepack/internal/instance"
	"stagepack/internal/pack"
	"stagepack/internal/save"
	"stagepack/internal/slidev"
	"stagepack/internal/ui"
	"stagepack/internal/workspace"
)

const fixture = "../../testdata/deck"

func packFixture(t *testing.T, dir string) string {
	t.Helper()
	out := filepath.Join(dir, "talk"+brand.Ext)
	if _, err := pack.Pack(pack.Options{Dir: fixture, Out: out, Warn: io.Discard}); err != nil {
		t.Fatal(err)
	}
	return out
}

func entryText(t *testing.T, deckPath, name string) string {
	t.Helper()
	zr, err := zip.OpenReader(deckPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	t.Fatalf("%s has no entry %s", deckPath, name)
	return ""
}

// fakeServer stands in for a running Slidev.
type fakeServer struct {
	port int
	base string
	done chan error
	once sync.Once
}

func (f *fakeServer) Port() int          { return f.port }
func (f *fakeServer) BaseURL() string    { return f.base }
func (f *fakeServer) Done() <-chan error { return f.done }
func (f *fakeServer) Stop() error        { f.once.Do(func() { f.done <- nil; close(f.done) }); return nil }
func newFakeServer(base string) *fakeServer {
	return &fakeServer{port: 4242, base: base, done: make(chan error, 1)}
}

// fakeSlidev replaces startSlidev for one test and records its calls.
type fakeSlidev struct {
	mu    sync.Mutex
	calls []slidev.Config
	srv   *fakeServer
}

func useFakeSlidev(t *testing.T) *fakeSlidev {
	t.Helper()
	f := &fakeSlidev{srv: newFakeServer("http://127.0.0.1:4242")}
	old := startSlidev
	startSlidev = func(ctx context.Context, c slidev.Config, port int) (server, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, c)
		return f.srv, nil
	}
	t.Cleanup(func() { startSlidev = old })
	useHeadmatter(t, `"default"`, `[]`, nil)
	return f
}

// useHeadmatter makes the app see this theme and these addons (JSON) in every
// talk's headmatter, or err, instead of running Slidev's parser.
func useHeadmatter(t *testing.T, theme, addons string, err error) {
	t.Helper()
	old := readHeadmatter
	readHeadmatter = func(ctx context.Context, node, runtimeDir, slidesPath string) (slidev.Headmatter, error) {
		return slidev.Headmatter{Theme: json.RawMessage(theme), Addons: json.RawMessage(addons)}, err
	}
	t.Cleanup(func() { readHeadmatter = old })
}

func (f *fakeSlidev) configs() []slidev.Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]slidev.Config(nil), f.calls...)
}

// fakeUI records what the app shows and lets the test drive the actions.
type fakeUI struct {
	mu       sync.Mutex
	acts     ui.Actions
	statuses []save.Status
	details  []string
	errs     []string
	notes    []string
	savePath string
	answer   bool     // what Confirm says
	onAsk    func()   // run while Confirm is up
	asked    []string // the questions Confirm was asked
	ran      bool
	quit     chan struct{}
	quitOnce sync.Once
	events   []string // "opening <name>", "opened", "error", "notify", "confirm", in order
	busy     int      // Opening boxes shown and not closed yet
}

func newFakeUI() *fakeUI { return &fakeUI{quit: make(chan struct{})} }

func (f *fakeUI) factory(a ui.Actions) ui.UI {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acts = a
	return f
}

func (f *fakeUI) Run(ready func()) {
	f.mu.Lock()
	f.ran = true
	f.mu.Unlock()
	ready()
	<-f.quit
}

func (f *fakeUI) SetStatus(title string, s save.Status, detail string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, s)
	f.details = append(f.details, detail)
}

func (f *fakeUI) Notify(title, msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = append(f.notes, msg)
	f.events = append(f.events, "notify")
}

func (f *fakeUI) Error(title, msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs = append(f.errs, msg)
	f.events = append(f.events, "error")
}

func (f *fakeUI) Opening(name string) (done func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.busy++
	f.events = append(f.events, "opening "+name)
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.busy--
		f.events = append(f.events, "opened")
	}
}

func (f *fakeUI) busyShown() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.busy > 0
}

// checkOpening wants the "Opening" box shown once, for name, and closed once, before
// any error was shown.
func (f *fakeUI) checkOpening(t *testing.T, name string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	opened, closed := -1, -1
	for i, e := range f.events {
		switch {
		case strings.HasPrefix(e, "opening "):
			if opened >= 0 || e != "opening "+name {
				t.Errorf("events %q: want one \"opening %s\"", f.events, name)
			}
			opened = i
		case e == "opened":
			if closed >= 0 {
				t.Errorf("events %q: the box was closed twice", f.events)
			}
			closed = i
		case e == "error" && closed < 0:
			t.Errorf("events %q: an error was shown with the Opening box still up", f.events)
		}
	}
	if opened < 0 || closed < opened {
		t.Errorf("events %q: want the Opening box shown and then closed", f.events)
	}
}

func (f *fakeUI) Confirm(title, msg string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.busy > 0 {
		f.events = append(f.events, "confirm with the Opening box up")
	}
	f.asked = append(f.asked, msg)
	f.events = append(f.events, "confirm")
	if f.onAsk != nil {
		f.mu.Unlock()
		f.onAsk()
		f.mu.Lock()
	}
	return f.answer
}

func (f *fakeUI) AskSavePath(suggest string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.savePath, f.savePath != ""
}

func (f *fakeUI) Quit() { f.quitOnce.Do(func() { close(f.quit) }) }

// waitStatus waits until the UI was shown a status other than Pending: the deck is running.
func (f *fakeUI) waitRunning(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for _, s := range f.statuses {
			if s != save.Pending {
				f.mu.Unlock()
				return
			}
		}
		f.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the deck never reported running")
}

// browserLog records the URLs the app opens, and whether the Opening box was up then.
type browserLog struct {
	mu     sync.Mutex
	urls   []string
	ui     *fakeUI
	busyAt []bool
}

func (b *browserLog) open(u string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.urls = append(b.urls, u)
	if b.ui != nil {
		b.busyAt = append(b.busyAt, b.ui.busyShown())
	}
	return nil
}

func (b *browserLog) list() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.urls...)
}

// openAsync runs Open in the background and returns a channel with its result.
func openAsync(o Options) <-chan error {
	res := make(chan error, 1)
	go func() { res <- Open(context.Background(), o) }()
	return res
}

func waitResult(t *testing.T, res <-chan error) error {
	t.Helper()
	select {
	case err := <-res:
		return err
	case <-time.After(20 * time.Second):
		t.Fatal("Open did not return")
		return nil
	}
}

func TestOpenSecondTimeReusesInstance(t *testing.T) {
	fs := useFakeSlidev(t)
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir())

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()
	_, h, err := instance.Claim(home, deckPath, func(instance.Record) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	defer h.Release()
	if err := h.Update(4242, ts.URL, filepath.Join(home, "somewhere")); err != nil {
		t.Fatal(err)
	}

	fu, br := newFakeUI(), &browserLog{}
	err = Open(context.Background(), Options{Home: home, DeckPath: deckPath, UI: fu.factory, Browser: br.open, GOOS: "linux", Env: os.Getenv})
	if err != nil {
		t.Fatal(err)
	}
	if got := br.list(); len(got) != 1 || got[0] != ts.URL+"/presenter/" {
		t.Errorf("browser opened %v, want [%s/presenter/]", got, ts.URL)
	}
	if n := len(fs.configs()); n != 0 {
		t.Errorf("Slidev started %d times, want 0", n)
	}
	if fu.ran {
		t.Error("the UI ran for a deck that is already open")
	}
}

// writeDeck writes a deck file holding only a manifest with the given Slidev version and a slides file.
func writeDeck(t *testing.T, path, slidevVersion string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	m := map[string]any{"format": 1, "id": "too-new", "title": "Too new", "slidev": slidevVersion, "entry": "slides.md",
		"created": time.Now().UTC(), "updated": time.Now().UTC()}
	data, _ := json.Marshal(m)
	for name, body := range map[string][]byte{brand.ManifestName: data, "slides.md": []byte("# hi\n")} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(body)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOpenTooNewShowsError(t *testing.T) {
	fs := useFakeSlidev(t)
	home := t.TempDir()
	deckPath := filepath.Join(t.TempDir(), "new"+brand.Ext)
	writeDeck(t, deckPath, "54.0.0")

	fu, br := newFakeUI(), &browserLog{}
	err := Open(context.Background(), Options{Home: home, DeckPath: deckPath, UI: fu.factory, Browser: br.open, GOOS: "linux", Env: os.Getenv})
	if err == nil {
		t.Fatal("Open succeeded on a deck for Slidev 54")
	}
	if len(fu.errs) != 1 || !strings.Contains(fu.errs[0], "newer "+brand.Name+" viewer") {
		t.Errorf("errors shown = %q, want one about a newer viewer", fu.errs)
	}
	fu.checkOpening(t, "new")
	if n := len(fs.configs()); n != 0 || len(br.list()) != 0 {
		t.Errorf("Slidev started %d times, browser %v; want neither", n, br.list())
	}
	if recs, _ := filepath.Glob(filepath.Join(home, "instances", "*.json")); len(recs) != 0 {
		t.Errorf("instance records left: %v", recs)
	}
}

// syncBuffer is a bytes.Buffer safe for one writer and one reader.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestOpenThrowawaySetsStatus(t *testing.T) {
	fs := useFakeSlidev(t)
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir()) // under os.TempDir(): a throwaway copy
	out := &syncBuffer{}
	var mu sync.Mutex
	var acts ui.Actions
	br := &browserLog{}
	res := openAsync(Options{Home: home, DeckPath: deckPath, Browser: br.open, GOOS: "linux", Env: os.Getenv,
		UI: func(a ui.Actions) ui.UI {
			mu.Lock()
			acts = a
			mu.Unlock()
			return ui.NewHeadless(out, a)
		}})

	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(out.String(), `"status":"throwaway"`) {
		if time.Now().After(deadline) {
			t.Fatalf("no throwaway status in:\n%s", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(out.String(), "temporary copy") {
		t.Errorf("throwaway status has no explanation:\n%s", out.String())
	}
	mu.Lock()
	stop := acts.Stop
	mu.Unlock()
	stop()
	if err := waitResult(t, res); err != nil {
		t.Fatal(err)
	}
	if got := br.list(); len(got) != 1 || got[0] != fs.srv.base+"/presenter/" {
		t.Errorf("browser opened %v", got)
	}
	cfgs := fs.configs()
	if len(cfgs) != 1 {
		t.Fatalf("Slidev started %d times", len(cfgs))
	}
	if _, err := os.Stat(cfgs[0].WorkDir); !os.IsNotExist(err) {
		t.Errorf("work folder %s left behind (%v)", cfgs[0].WorkDir, err)
	}
	if recs, _ := filepath.Glob(filepath.Join(home, "instances", "*.json")); len(recs) != 0 {
		t.Errorf("instance records left: %v", recs)
	}
}

func TestSaveCopySwitchesTarget(t *testing.T) {
	fs := useFakeSlidev(t)
	home := t.TempDir()
	tmp := t.TempDir()
	deckPath := packFixture(t, tmp)
	copyPath := filepath.Join(tmp, "copy"+brand.Ext)
	before := entryText(t, deckPath, "slides.md")

	fu, br := newFakeUI(), &browserLog{}
	fu.savePath = copyPath
	res := openAsync(Options{Home: home, DeckPath: deckPath, UI: fu.factory, Browser: br.open, GOOS: "linux", Env: os.Getenv})
	fu.waitRunning(t)

	fu.acts.SaveCopy()
	if _, err := os.Stat(copyPath); err != nil {
		t.Fatalf("no copy: %v", err)
	}
	cfg := fs.configs()[0]
	slides := filepath.Join(cfg.WorkDir, cfg.Entry)
	if err := os.WriteFile(slides, []byte(before+"\n<!-- note after the copy -->\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fu.acts.Stop() // flushes
	if err := waitResult(t, res); err != nil {
		t.Fatal(err)
	}

	if got := entryText(t, copyPath, "slides.md"); !strings.Contains(got, "note after the copy") {
		t.Errorf("the copy did not get the edit:\n%s", got)
	}
	if got := entryText(t, deckPath, "slides.md"); got != before {
		t.Errorf("the original changed after Save a copy:\n%s", got)
	}
	if len(fu.errs) != 0 {
		t.Errorf("errors shown: %q", fu.errs)
	}
	if recs, _ := filepath.Glob(filepath.Join(home, "instances", "*.json")); len(recs) != 0 {
		t.Errorf("instance records left: %v", recs)
	}
	if ents, _ := os.ReadDir(filepath.Join(home, "journal")); len(ents) != 0 {
		t.Errorf("journal not empty after a clean stop: %v", ents)
	}
}

func TestSlidevExitingStops(t *testing.T) {
	fs := useFakeSlidev(t)
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir())
	fu, br := newFakeUI(), &browserLog{}
	res := openAsync(Options{Home: home, DeckPath: deckPath, UI: fu.factory, Browser: br.open, GOOS: "linux", Env: os.Getenv})
	fu.waitRunning(t)

	cfg := fs.configs()[0]
	os.WriteFile(cfg.LogPath, []byte("vite: something broke\n"), 0o644)
	fs.srv.once.Do(func() { fs.srv.done <- errors.New("exit status 1"); close(fs.srv.done) })
	if err := waitResult(t, res); err == nil {
		t.Error("Open returned nil after Slidev died")
	}
	if len(fu.errs) != 1 || !strings.Contains(fu.errs[0], "something broke") {
		t.Errorf("errors shown = %q, want one with the log tail", fu.errs)
	}
	if _, err := os.Stat(cfg.WorkDir); !os.IsNotExist(err) {
		t.Errorf("work folder left behind (%v)", err)
	}
}

func TestSaveCopyRebuildsWhenSourceGone(t *testing.T) {
	fs := useFakeSlidev(t)
	home := t.TempDir()
	tmp := t.TempDir()
	deckPath := packFixture(t, tmp)
	copyPath := filepath.Join(t.TempDir(), "rescued"+brand.Ext)

	fu, br := newFakeUI(), &browserLog{}
	fu.savePath = copyPath
	res := openAsync(Options{Home: home, DeckPath: deckPath, UI: fu.factory, Browser: br.open, GOOS: "linux", Env: os.Getenv})
	fu.waitRunning(t)
	cfg := fs.configs()[0]
	slides := filepath.Join(cfg.WorkDir, cfg.Entry)
	before, _ := os.ReadFile(slides)
	if err := os.WriteFile(slides, append(before, "\n<!-- note before the stick was pulled -->\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(deckPath); err != nil { // the USB stick is pulled
		t.Fatal(err)
	}
	fu.acts.SaveCopy()
	fu.acts.Stop()
	if err := waitResult(t, res); err != nil {
		t.Fatal(err)
	}
	if len(fu.errs) != 0 {
		t.Errorf("errors shown: %q", fu.errs)
	}

	w, err := workspace.Extract(copyPath, t.TempDir())
	if err != nil {
		t.Fatalf("the rescued copy does not open: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(w.Dir, "slides.md")); !strings.Contains(string(got), "before the stick was pulled") {
		t.Errorf("the copy has no edit:\n%s", got)
	}
	want, _ := os.ReadFile(filepath.Join(fixture, "public", "clip.mp4"))
	if got, _ := os.ReadFile(filepath.Join(w.Dir, "public", "clip.mp4")); !bytes.Equal(got, want) {
		t.Error("clip.mp4 differs in the copy")
	}
}

// corruptJournal writes an unreadable journal entry for the deck and returns its path.
func corruptJournal(t *testing.T, home, deckPath string) string {
	t.Helper()
	zr, err := zip.OpenReader(deckPath)
	if err != nil {
		t.Fatal(err)
	}
	m, err := deck.ReadManifest(&zr.Reader)
	zr.Close()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, "journal", save.JournalKey(m.ID, deckPath)+".json")
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte("{half an entry"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRecoverFailureSetsEntryAside(t *testing.T) {
	useFakeSlidev(t)
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir())
	entry := corruptJournal(t, home, deckPath)

	fu, br := newFakeUI(), &browserLog{}
	res := openAsync(Options{Home: home, DeckPath: deckPath, UI: fu.factory, Browser: br.open, GOOS: "linux", Env: os.Getenv})
	fu.waitRunning(t)
	fu.acts.Stop()
	if err := waitResult(t, res); err != nil {
		t.Fatal(err)
	}
	if len(fu.notes) != 1 || !strings.Contains(fu.notes[0], ".kept-") {
		t.Fatalf("notices = %q, want one naming the set-aside file", fu.notes)
	}
	kept := fu.notes[0][strings.LastIndex(fu.notes[0], "kept in ")+len("kept in "):]
	if data, err := os.ReadFile(kept); err != nil || string(data) != "{half an entry" {
		t.Errorf("set-aside file %s = %q, %v", kept, data, err)
	}
	if _, err := os.Stat(entry); !os.IsNotExist(err) {
		t.Errorf("the unreadable entry is still under the deck's key (%v)", err)
	}
}

func TestRecoverFailureThatCannotBeSetAsideStops(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores folder permissions")
	}
	fs := useFakeSlidev(t)
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir())
	entry := corruptJournal(t, home, deckPath)
	jdir := filepath.Dir(entry)
	if err := os.Chmod(jdir, 0o555); err != nil { // nothing can be moved out of the journal
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(jdir, 0o755) })

	fu, br := newFakeUI(), &browserLog{}
	err := Open(context.Background(), Options{Home: home, DeckPath: deckPath, UI: fu.factory, Browser: br.open, GOOS: "linux", Env: os.Getenv})
	if err == nil {
		t.Fatal("Open succeeded over an unrecoverable journal entry")
	}
	if len(fu.errs) != 1 || !strings.Contains(fu.errs[0], entry) {
		t.Errorf("errors shown = %q, want one naming %s", fu.errs, entry)
	}
	fu.checkOpening(t, "talk")
	if n := len(fs.configs()); n != 0 || fu.ran {
		t.Errorf("Slidev started %d times, UI ran %v; want neither", n, fu.ran)
	}
	if left, _ := filepath.Glob(filepath.Join(home, "runtimes", "*", "work", "*")); len(left) != 0 {
		t.Errorf("work folders left: %v", left)
	}
	if recs, _ := filepath.Glob(filepath.Join(home, "instances", "*.json")); len(recs) != 0 {
		t.Errorf("instance records left: %v", recs)
	}
	if data, _ := os.ReadFile(entry); string(data) != "{half an entry" {
		t.Errorf("the entry changed: %q", data)
	}
}

func TestOpeningShownUntilTheBrowserOpens(t *testing.T) {
	useFakeSlidev(t)
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir())
	fu := newFakeUI()
	br := &browserLog{ui: fu}
	res := openAsync(Options{Home: home, DeckPath: deckPath, UI: fu.factory, Browser: br.open, GOOS: "linux", Env: os.Getenv})
	fu.waitRunning(t)
	br.mu.Lock()
	busyAt := append([]bool(nil), br.busyAt...)
	br.mu.Unlock()
	if len(busyAt) != 1 || !busyAt[0] {
		t.Errorf("Opening box up when the browser opened: %v, want [true]", busyAt)
	}
	if fu.busyShown() {
		t.Error("Opening box still up once the talk runs")
	}
	fu.acts.Stop()
	if err := waitResult(t, res); err != nil {
		t.Fatal(err)
	}
	fu.checkOpening(t, "talk")
}

func TestOpeningClosedOnSecondLaunch(t *testing.T) {
	useFakeSlidev(t)
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir())
	_, h, err := instance.Claim(home, deckPath, func(instance.Record) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	defer h.Release()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()
	if err := h.Update(4242, ts.URL, filepath.Join(home, "somewhere")); err != nil {
		t.Fatal(err)
	}
	fu := newFakeUI()
	br := &browserLog{ui: fu}
	if err := Open(context.Background(), Options{Home: home, DeckPath: deckPath, UI: fu.factory, Browser: br.open, GOOS: "linux", Env: os.Getenv}); err != nil {
		t.Fatal(err)
	}
	fu.checkOpening(t, "talk")
}

func TestSecondLaunchWhileStartingSaysSo(t *testing.T) {
	fs := useFakeSlidev(t)
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir())
	// The first launch has claimed the deck but has no URL yet: it is still starting.
	_, h, err := instance.Claim(home, deckPath, func(instance.Record) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	defer h.Release()

	fu, br := newFakeUI(), &browserLog{}
	if err := Open(context.Background(), Options{Home: home, DeckPath: deckPath, UI: fu.factory, Browser: br.open, GOOS: "linux", Env: os.Getenv}); err != nil {
		t.Fatal(err)
	}
	if len(fu.notes) != 1 || fu.notes[0] != "talk is already opening…" {
		t.Errorf("notices = %q, want [\"talk is already opening…\"]", fu.notes)
	}
	if len(br.list()) != 0 || len(fs.configs()) != 0 || fu.ran {
		t.Errorf("browser %v, Slidev %d, UI ran %v; want none", br.list(), len(fs.configs()), fu.ran)
	}
	fu.checkOpening(t, "talk")
}

func TestOpeningClosedWhenSlidevFailsToStart(t *testing.T) {
	old := startSlidev
	startSlidev = func(ctx context.Context, c slidev.Config, port int) (server, error) {
		return nil, errors.New("no node here")
	}
	t.Cleanup(func() { startSlidev = old })
	useHeadmatter(t, `"default"`, `[]`, nil)
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir())
	fu, br := newFakeUI(), &browserLog{}
	err := waitResult(t, openAsync(Options{Home: home, DeckPath: deckPath, UI: fu.factory, Browser: br.open, GOOS: "linux", Env: os.Getenv}))
	if err == nil {
		t.Fatal("Open returned nil though Slidev did not start")
	}
	if len(fu.errs) != 1 || !strings.Contains(fu.errs[0], "no node here") {
		t.Errorf("errors shown = %q", fu.errs)
	}
	fu.checkOpening(t, "talk")
}
