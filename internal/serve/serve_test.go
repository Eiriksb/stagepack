package serve

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
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
	"stagepack/internal/trust"
	"stagepack/internal/workspace"
	slidevrt "stagepack/runtime"
)

const fixture = "../../testdata/deck"

// testNow is the clock Run gets: a save stamps the manifest with it. The nanoseconds check that
// the stamp survives the trip through the file (a second save would see a conflict otherwise).
var testNow = time.Date(2026, 11, 9, 12, 0, 0, 123456789, time.UTC)

const wantConflict = "This file was changed by another program since it was opened, so it was not overwritten."

func TestMain(m *testing.M) {
	// Slidev's parser is not run: every talk's headmatter has the bundled theme and no addons.
	readHeadmatter = func(ctx context.Context, node, runtimeDir, slidesPath string) (slidev.Headmatter, error) {
		return slidev.Headmatter{Theme: json.RawMessage(`"default"`), Addons: json.RawMessage(`[]`)}, nil
	}
	os.Exit(m.Run())
}

// packFixture packs testdata/deck into dir/name, with extra files added to it.
func packFixture(t *testing.T, dir, name string, extra map[string]string) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "talk")
	if err := os.CopyFS(src, os.DirFS(fixture)); err != nil {
		t.Fatal(err)
	}
	for p, body := range extra {
		if err := os.WriteFile(filepath.Join(src, filepath.FromSlash(p)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, name)
	if _, err := pack.Pack(pack.Options{Dir: src, Out: out, Warn: io.Discard}); err != nil {
		t.Fatal(err)
	}
	return out
}

// codeFiles make the fixture a talk that contains code.
var codeFiles = map[string]string{"vite.config.ts": "export default {}\n"}

// version is the deck's ID and the version hash Run computes for it.
func version(t *testing.T, deckPath string) (id, hash string) {
	t.Helper()
	w, err := workspace.Extract(deckPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return w.Manifest.ID, w.Hash
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

func manifestOf(t *testing.T, deckPath string) deck.Manifest {
	t.Helper()
	zr, err := zip.OpenReader(deckPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	m, err := deck.ReadManifest(&zr.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// fakeServer stands in for a running Slidev.
type fakeServer struct {
	port int
	done chan error
	once sync.Once
}

func (f *fakeServer) Port() int          { return f.port }
func (f *fakeServer) BaseURL() string    { return "http://127.0.0.1:" + strconv.Itoa(f.port) }
func (f *fakeServer) Done() <-chan error { return f.done }
func (f *fakeServer) Stop() error        { f.exit(nil); return nil }

// exit makes Slidev end with err; Stop's exit is nil.
func (f *fakeServer) exit(err error) { f.once.Do(func() { f.done <- err; close(f.done) }) }

// fakeStart replaces slidev.Start: its first calls fail with fail, the next one succeeds.
type fakeStart struct {
	mu    sync.Mutex
	fail  []error
	calls []slidev.Config
	ports []int
	srv   *fakeServer
	// hang, when set, makes Start wait for its context, as a slow Slidev does; it is closed
	// when Start is waiting.
	hang chan struct{}
}

func (f *fakeStart) start(ctx context.Context, c slidev.Config, port int) (Server, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	f.ports = append(f.ports, port)
	if f.hang != nil {
		close(f.hang)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if n := len(f.calls); n <= len(f.fail) {
		return nil, f.fail[n-1]
	}
	f.srv = &fakeServer{port: port, done: make(chan error, 1)}
	return f.srv, nil
}

func (f *fakeStart) configs() []slidev.Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]slidev.Config(nil), f.calls...)
}

func (f *fakeStart) server() *fakeServer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.srv
}

// harness runs Run on its own goroutine with pipes for stdin and stdout.
type harness struct {
	t      *testing.T
	o      Options
	in     *io.PipeWriter
	lines  chan string
	exit   chan int
	code   *int
	fake   *fakeStart
	cancel context.CancelFunc
}

// serveDeck runs Run on o with a fake Slidev whose first starts fail with fail.
func serveDeck(t *testing.T, o Options, fail ...error) *harness {
	t.Helper()
	return serveWith(t, o, &fakeStart{fail: fail})
}

func serveWith(t *testing.T, o Options, fake *fakeStart) *harness {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{t: t, o: o, in: inW, lines: make(chan string, 100), exit: make(chan int, 1), fake: fake, cancel: cancel}
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			h.lines <- sc.Text()
		}
		close(h.lines)
	}()
	go func() {
		code := Run(ctx, o, inR, outW, Deps{Start: h.fake.start, Now: func() time.Time { return testNow }})
		outW.Close()
		inR.Close()
		h.exit <- code
	}()
	t.Cleanup(func() {
		inW.Close()
		cancel()
		if h.code == nil {
			select {
			case <-h.exit:
			case <-time.After(10 * time.Second):
				t.Error("Run did not return after stdin closed")
			}
		}
	})
	return h
}

// next is the next event, as a map, with the line it came from.
func (h *harness) next() (map[string]any, string) {
	h.t.Helper()
	select {
	case line, ok := <-h.lines:
		if !ok {
			h.t.Fatal("stdout ended; want another event")
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			h.t.Fatalf("stdout line %q is not JSON: %v", line, err)
		}
		if _, ok := ev["event"].(string); !ok {
			h.t.Fatalf("stdout line %q has no event", line)
		}
		return ev, line
	case <-time.After(10 * time.Second):
		h.t.Fatal("no event within 10 s")
	}
	return nil, ""
}

// expect is the next event, which must be name.
func (h *harness) expect(name string) map[string]any {
	h.t.Helper()
	ev, line := h.next()
	if ev["event"] != name {
		h.t.Fatalf("event = %s, want %s", line, name)
	}
	return ev
}

func (h *harness) send(line string) {
	h.t.Helper()
	if _, err := io.WriteString(h.in, line+"\n"); err != nil {
		h.t.Fatalf("send %s: %v", line, err)
	}
}

// closeStdin is the editor going away.
func (h *harness) closeStdin() { h.in.Close() }

// wait returns Run's exit code, and checks no event follows the ones already read.
func (h *harness) wait() int {
	h.t.Helper()
	if h.code == nil {
		select {
		case c := <-h.exit:
			h.code = &c
		case <-time.After(10 * time.Second):
			h.t.Fatal("Run did not return within 10 s")
		}
	}
	for line := range h.lines {
		h.t.Errorf("unexpected event after the last one: %s", line)
	}
	return *h.code
}

func rtDir(home string) string {
	return filepath.Join(home, "runtimes", strconv.Itoa(slidevrt.Major()))
}

// workFolders lists the work folders under home's runtime.
func workFolders(t *testing.T, home string) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(rtDir(home), "work"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func checkCleanedUp(t *testing.T, home, workDir string) {
	t.Helper()
	if _, err := os.Stat(workDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the work folder %s is still there (err %v)", workDir, err)
	}
	if left := workFolders(t, home); len(left) > 0 {
		t.Errorf("work folders left: %q", left)
	}
	if recs := instance.List(home); len(recs) > 0 {
		t.Errorf("instance records left: %+v", recs)
	}
}

func str(t *testing.T, ev map[string]any, key string) string {
	t.Helper()
	s, ok := ev[key].(string)
	if !ok {
		t.Fatalf("%s: %q is %v, want a string", ev["event"], key, ev[key])
	}
	return s
}

// strs is a JSON array of strings, which must be there (not null).
func strs(t *testing.T, ev map[string]any, key string) []string {
	t.Helper()
	a, ok := ev[key].([]any)
	if !ok {
		t.Fatalf("%s: %q is %v, want an array", ev["event"], key, ev[key])
	}
	out := []string{}
	for _, v := range a {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("%s: %q holds %v, want strings", ev["event"], key, v)
		}
		out = append(out, s)
	}
	return out
}

// checkUnpacked checks the unpacked event and returns the work folder. (Run may have removed it
// already, if it failed right after.)
func checkUnpacked(t *testing.T, ev map[string]any, home, deckPath string, trusted bool) string {
	t.Helper()
	m := manifestOf(t, deckPath)
	if str(t, ev, "id") != m.ID || str(t, ev, "title") != m.Title || str(t, ev, "entry") != "slides.md" {
		t.Errorf("unpacked = %v, want id %s, title %q, entry slides.md", ev, m.ID, m.Title)
	}
	if ev["trusted"] != trusted {
		t.Errorf("unpacked trusted = %v, want %v", ev["trusted"], trusted)
	}
	workDir := str(t, ev, "workDir")
	if !filepath.IsAbs(workDir) || filepath.Dir(workDir) != filepath.Join(rtDir(home), "work") {
		t.Errorf("workDir = %s, want a folder in %s", workDir, filepath.Join(rtDir(home), "work"))
	}
	return workDir
}

// checkReady checks the ready event against the fake Slidev.
func checkReady(t *testing.T, h *harness, ev map[string]any, workDir string) {
	t.Helper()
	srv := h.fake.server()
	if srv == nil {
		t.Fatal("ready, but Slidev was not started")
	}
	if ev["port"] != float64(srv.port) || str(t, ev, "baseUrl") != srv.BaseURL() || str(t, ev, "workDir") != workDir {
		t.Errorf("ready = %v, want port %d, baseUrl %s, workDir %s", ev, srv.port, srv.BaseURL(), workDir)
	}
	if _, err := os.Stat(filepath.Join(workDir, "slides.md")); err != nil {
		t.Errorf("the work folder has no slides.md: %v", err)
	}
}

func TestServeNoCode(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, nil)
	h := serveDeck(t, Options{Home: home, DeckPath: deckPath})

	ev, line := h.next()
	if ev["event"] != "unpacked" {
		t.Fatalf("first event = %s, want unpacked", line)
	}
	if !strings.Contains(line, `"code":[]`) {
		t.Errorf("unpacked = %s, want \"code\":[]", line)
	}
	workDir := checkUnpacked(t, ev, home, deckPath, true)
	checkReady(t, h, h.expect("ready"), workDir)

	// Slidev runs in the work folder, its log where the viewer keeps it, and no preload without addons.
	cfgs := h.fake.configs()
	if len(cfgs) != 1 {
		t.Fatalf("Start called %d times, want 1", len(cfgs))
	}
	c := cfgs[0]
	m := manifestOf(t, deckPath)
	wantLog := filepath.Join(home, "logs", save.JournalKey(m.ID, deckPath)+".log")
	wantCLI := filepath.Join(rtDir(home), "node_modules", "@slidev", "cli", "bin", "slidev.mjs")
	if c.WorkDir != workDir || c.Entry != "slides.md" || c.LogPath != wantLog || c.CLI != wantCLI || c.NodeArgs != nil || c.Env != nil {
		t.Errorf("Start got %+v; want work folder %s, log %s, CLI %s, no NodeArgs or Env", c, workDir, wantLog, wantCLI)
	}
	if _, err := os.Stat(filepath.Dir(wantLog)); err != nil {
		t.Errorf("the logs folder was not made: %v", err)
	}

	// While it serves, the instance record names this process, Slidev's address and the work folder.
	recs := instance.List(home)
	if len(recs) != 1 || recs[0].PID != os.Getpid() || recs[0].Port != h.fake.server().port ||
		recs[0].BaseURL != h.fake.server().BaseURL() || recs[0].WorkDir != workDir {
		t.Errorf("instance records = %+v", recs)
	}

	h.closeStdin()
	h.expect("stopped")
	if code := h.wait(); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
	checkCleanedUp(t, home, workDir)
	if _, err := os.Stat(filepath.Join(home, trust.FileName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a talk without code was added to the trusted list (err %v)", err)
	}
}

func TestServeCodeAskedYes(t *testing.T) {
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, codeFiles)
	id, hash := version(t, deckPath)
	h := serveDeck(t, Options{Home: home, DeckPath: deckPath})

	ev := h.expect("unpacked")
	workDir := checkUnpacked(t, ev, home, deckPath, false)
	if code := strs(t, ev, "code"); len(code) == 0 || !strings.Contains(strings.Join(code, "\n"), "vite.config.ts") {
		t.Errorf("unpacked code = %q, want it to name vite.config.ts", code)
	}
	q := str(t, h.expect("confirm"), "question")
	if !strings.Contains(q, "contains code") || !strings.Contains(q, "vite.config.ts") || !strings.Contains(q, "talk"+brand.Ext) {
		t.Errorf("question = %q", q)
	}
	if n := len(h.fake.configs()); n != 0 {
		t.Fatalf("Slidev was started before the answer (%d calls)", n)
	}

	h.send(`{"answer":"yes"}`)
	checkReady(t, h, h.expect("ready"), workDir)
	if !(trust.Store{Path: filepath.Join(home, trust.FileName)}).Trusted(id, hash) {
		t.Errorf("trusted.json does not hold this version of the talk")
	}

	h.closeStdin()
	h.expect("stopped")
	if code := h.wait(); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
	checkCleanedUp(t, home, workDir)
}

func TestServeCodeAskedNo(t *testing.T) {
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, codeFiles)
	id, hash := version(t, deckPath)
	h := serveDeck(t, Options{Home: home, DeckPath: deckPath})

	workDir := checkUnpacked(t, h.expect("unpacked"), home, deckPath, false)
	h.expect("confirm")
	h.send(`{"answer":"no"}`)
	if code := h.wait(); code != 3 {
		t.Errorf("exit %d, want 3", code)
	}
	if n := len(h.fake.configs()); n != 0 {
		t.Errorf("Slidev was started %d times after a no", n)
	}
	checkCleanedUp(t, home, workDir)
	if (trust.Store{Path: filepath.Join(home, trust.FileName)}).Trusted(id, hash) {
		t.Error("a no added the talk to the trusted list")
	}
}

// While the question is up, another launch may judge the claim dead and take the talk over:
// a yes then hands over to it, as busy, and leaves its record alone.
func TestServeTakenOverWhileAsking(t *testing.T) {
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, codeFiles)
	h := serveDeck(t, Options{Home: home, DeckPath: deckPath})
	workDir := checkUnpacked(t, h.expect("unpacked"), home, deckPath, false)
	h.expect("confirm")

	viewer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer viewer.Close()
	u, _ := url.Parse(viewer.URL)
	port, _ := strconv.Atoi(u.Port())
	_, other, err := instance.Claim(home, deckPath, func(instance.Record) bool { return false })
	if err != nil || other == nil {
		t.Fatalf("take over: %v", err)
	}
	if err := other.Update(port, viewer.URL, "/elsewhere/work"); err != nil {
		t.Fatal(err)
	}

	h.send(`{"answer":"yes"}`)
	if got := str(t, h.expect("busy"), "baseUrl"); got != viewer.URL {
		t.Errorf("busy baseUrl = %s, want %s", got, viewer.URL)
	}
	if code := h.wait(); code != 4 {
		t.Errorf("exit %d, want 4", code)
	}
	if n := len(h.fake.configs()); n != 0 {
		t.Errorf("Slidev was started %d times", n)
	}
	if _, err := os.Stat(workDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the work folder is still there (err %v)", err)
	}
	if recs := instance.List(home); len(recs) != 1 || recs[0].BaseURL != viewer.URL {
		t.Errorf("the other launch's record was changed: %+v", recs)
	}
}

func TestServeStopWhileAsking(t *testing.T) {
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, codeFiles)
	h := serveDeck(t, Options{Home: home, DeckPath: deckPath})

	workDir := checkUnpacked(t, h.expect("unpacked"), home, deckPath, false)
	h.expect("confirm")
	h.send(`{"cmd":"save"}`) // nothing to save before it is open
	if names := strs(t, h.expect("save-failed"), "names"); len(names) != 0 {
		t.Errorf("save-failed names = %q, want none", names)
	}
	h.closeStdin()
	h.expect("stopped")
	if code := h.wait(); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
	if n := len(h.fake.configs()); n != 0 {
		t.Errorf("Slidev was started %d times", n)
	}
	checkCleanedUp(t, home, workDir)
}

func TestServeSave(t *testing.T) {
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, nil)
	h := serveDeck(t, Options{Home: home, DeckPath: deckPath})
	workDir := checkUnpacked(t, h.expect("unpacked"), home, deckPath, true)
	h.expect("ready")

	slides := filepath.Join(workDir, "slides.md")
	data, err := os.ReadFile(slides)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(data), "Hello from the fixture.", "Hello from the editor.", 1)
	if err := os.WriteFile(slides, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	h.send(`{"cmd":"save"}`)
	if got := str(t, h.expect("saved"), "updated"); got != testNow.Format(time.RFC3339Nano) {
		t.Errorf("saved updated = %s, want %s", got, testNow.Format(time.RFC3339Nano))
	}
	if got := entryText(t, deckPath, "slides.md"); got != edited {
		t.Errorf("the deck's slides.md = %q, want the edit", got)
	}
	if m := manifestOf(t, deckPath); !m.Updated.Equal(testNow) {
		t.Errorf("manifest updated = %v, want %v", m.Updated, testNow)
	}

	// The second save starts from the first: no conflict with itself.
	if err := os.WriteFile(slides, []byte(edited+"\nMore.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.send(`{"cmd":"save"}`)
	h.expect("saved")
	if got := entryText(t, deckPath, "slides.md"); got != edited+"\nMore.\n" {
		t.Errorf("after the second save the deck's slides.md = %q", got)
	}

	h.send(`{"cmd":"stop"}`)
	h.expect("stopped")
	if code := h.wait(); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
	checkCleanedUp(t, home, workDir)
}

func TestServeSaveKeepsTrust(t *testing.T) {
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, codeFiles)
	id, hash := version(t, deckPath)
	store := trust.Store{Path: filepath.Join(home, trust.FileName)}
	if err := store.Remember(id, hash, "Fixture talk"); err != nil {
		t.Fatal(err)
	}
	h := serveDeck(t, Options{Home: home, DeckPath: deckPath})
	ev := h.expect("unpacked")
	workDir := checkUnpacked(t, ev, home, deckPath, true)
	if len(strs(t, ev, "code")) == 0 {
		t.Error("unpacked code is empty for a talk with code")
	}
	h.expect("ready") // no question: this version is trusted

	if err := os.WriteFile(filepath.Join(workDir, "public", "new.png"), []byte("\x89PNG new image"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.send(`{"cmd":"save"}`)
	h.expect("saved")
	_, newHash := version(t, deckPath)
	if newHash == hash {
		t.Fatal("a new file in public/ kept the version hash")
	}
	if !store.Trusted(id, newHash) {
		t.Error("the saved version is not trusted: the next open would ask again")
	}
	h.closeStdin()
	h.expect("stopped")
	if code := h.wait(); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
}

// --trust-code runs the code without asking, and without adding the talk to the list, also on save.
func TestServeTrustCodeDoesNotRemember(t *testing.T) {
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, codeFiles)
	id, hash := version(t, deckPath)
	h := serveDeck(t, Options{Home: home, DeckPath: deckPath, TrustCode: true})
	workDir := checkUnpacked(t, h.expect("unpacked"), home, deckPath, true)
	h.expect("ready")
	if err := os.WriteFile(filepath.Join(workDir, "public", "new.png"), []byte("\x89PNG new image"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.send(`{"cmd":"save"}`)
	h.expect("saved")
	_, newHash := version(t, deckPath)
	store := trust.Store{Path: filepath.Join(home, trust.FileName)}
	if store.Trusted(id, hash) || store.Trusted(id, newHash) {
		t.Error("--trust-code added the talk to the trusted list")
	}
	h.closeStdin()
	h.expect("stopped")
	h.wait()
}

func TestServeConflict(t *testing.T) {
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, nil)
	h := serveDeck(t, Options{Home: home, DeckPath: deckPath})
	workDir := checkUnpacked(t, h.expect("unpacked"), home, deckPath, true)
	h.expect("ready")

	// Another program rewrites the deck underneath.
	if err := save.RewriteEntry(deckPath, "slides.md", []byte("# Changed elsewhere\n"), testNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(deckPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "slides.md"), []byte("# Edited here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.send(`{"cmd":"save"}`)
	ev := h.expect("conflict")
	if got := str(t, ev, "message"); got != wantConflict {
		t.Errorf("conflict message = %q, want %q", got, wantConflict)
	}
	if after, err := os.ReadFile(deckPath); err != nil || !bytes.Equal(after, before) {
		t.Errorf("the deck changed after a conflict (err %v)", err)
	}

	// A failed save never stops serving.
	h.send(`{"cmd":"stop"}`)
	h.expect("stopped")
	if code := h.wait(); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
}

func TestServeSaveFailed(t *testing.T) {
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, nil)
	h := serveDeck(t, Options{Home: home, DeckPath: deckPath})
	workDir := checkUnpacked(t, h.expect("unpacked"), home, deckPath, true)
	h.expect("ready")
	before, err := os.ReadFile(deckPath)
	if err != nil {
		t.Fatal(err)
	}

	if runtime.GOOS != "windows" { // Windows can't make a file with ':' in its name
		bad := filepath.Join(workDir, "public", "a:b.png")
		if err := os.WriteFile(bad, []byte("png"), 0o644); err != nil {
			t.Fatal(err)
		}
		h.send(`{"cmd":"save"}`)
		ev := h.expect("save-failed")
		if names := strs(t, ev, "names"); !reflect.DeepEqual(names, []string{"public/a:b.png"}) {
			t.Errorf("save-failed names = %q, want [public/a:b.png]", names)
		}
		if msg := str(t, ev, "message"); !strings.Contains(msg, "public/a:b.png") {
			t.Errorf("save-failed message = %q, want it to name the file", msg)
		}
		if err := os.Remove(bad); err != nil {
			t.Fatal(err)
		}
	}

	slides := filepath.Join(workDir, "slides.md")
	data, err := os.ReadFile(slides)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(slides); err != nil {
		t.Fatal(err)
	}
	h.send(`{"cmd":"save"}`)
	ev := h.expect("save-failed")
	if names := strs(t, ev, "names"); len(names) != 0 {
		t.Errorf("save-failed names = %q, want none", names)
	}
	if msg := str(t, ev, "message"); !strings.Contains(msg, "slides.md") {
		t.Errorf("save-failed message = %q, want it to name slides.md", msg)
	}
	if after, err := os.ReadFile(deckPath); err != nil || !bytes.Equal(after, before) {
		t.Errorf("the deck changed after a failed save (err %v)", err)
	}

	// Still serving: put it back and save.
	if err := os.WriteFile(slides, data, 0o644); err != nil {
		t.Fatal(err)
	}
	h.send(`{"cmd":"save"}`)
	h.expect("saved")
	h.closeStdin()
	h.expect("stopped")
	if code := h.wait(); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
}

func TestServeBusy(t *testing.T) {
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, nil)
	// A live viewer has the deck open.
	viewer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer viewer.Close()
	u, _ := url.Parse(viewer.URL)
	port, _ := strconv.Atoi(u.Port())
	existing, other, err := instance.Claim(home, deckPath, instance.Alive)
	if err != nil || existing != nil {
		t.Fatalf("claim: %v, %+v", err, existing)
	}
	if err := other.Update(port, viewer.URL, "/elsewhere/work"); err != nil {
		t.Fatal(err)
	}

	h := serveDeck(t, Options{Home: home, DeckPath: deckPath})
	if got := str(t, h.expect("busy"), "baseUrl"); got != viewer.URL {
		t.Errorf("busy baseUrl = %s, want %s", got, viewer.URL)
	}
	if code := h.wait(); code != 4 {
		t.Errorf("exit %d, want 4", code)
	}
	if n := len(h.fake.configs()); n != 0 {
		t.Errorf("Slidev was started %d times", n)
	}
	if left := workFolders(t, home); len(left) > 0 {
		t.Errorf("the deck was unpacked: %q", left)
	}
	if recs := instance.List(home); len(recs) != 1 || recs[0].BaseURL != viewer.URL {
		t.Errorf("the viewer's record was changed: %+v", recs)
	}
}

func TestServeSlidevDies(t *testing.T) {
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, nil)
	h := serveDeck(t, Options{Home: home, DeckPath: deckPath})
	workDir := checkUnpacked(t, h.expect("unpacked"), home, deckPath, true)
	h.expect("ready")

	logPath := h.fake.configs()[0].LogPath
	if err := os.WriteFile(logPath, []byte("vite v7\nError: the server crashed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.fake.server().exit(errors.New("exit status 1"))
	ev := h.expect("error")
	if msg := str(t, ev, "message"); msg == "" {
		t.Error("error message is empty")
	}
	if log := str(t, ev, "log"); !strings.Contains(log, "Error: the server crashed") {
		t.Errorf("error log = %q, want the end of Slidev's log", log)
	}
	h.expect("stopped")
	if code := h.wait(); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	checkCleanedUp(t, home, workDir)
}

func TestServeStartFails(t *testing.T) {
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, nil)
	h := serveDeck(t, Options{Home: home, DeckPath: deckPath}, errors.New("start /home/node/bin/node: no such file or directory"))
	workDir := checkUnpacked(t, h.expect("unpacked"), home, deckPath, true)
	ev := h.expect("error")
	if msg := str(t, ev, "message"); !strings.Contains(msg, "no such file") {
		t.Errorf("error message = %q, want Start's error", msg)
	}
	str(t, ev, "log")
	h.expect("stopped")
	if code := h.wait(); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if n := len(h.fake.configs()); n != 1 {
		t.Errorf("Start called %d times, want 1: only a lost port race is retried", n)
	}
	checkCleanedUp(t, home, workDir)
}

// Stdin can stop it while Slidev is still starting; a save then has nothing to save.
func TestServeStopWhileStarting(t *testing.T) {
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, nil)
	hanging := make(chan struct{})
	h := serveWith(t, Options{Home: home, DeckPath: deckPath}, &fakeStart{hang: hanging})
	workDir := checkUnpacked(t, h.expect("unpacked"), home, deckPath, true)
	select {
	case <-hanging:
	case <-time.After(10 * time.Second):
		t.Fatal("Slidev was not started")
	}
	h.send(`{"cmd":"save"}`)
	if msg := str(t, h.expect("save-failed"), "message"); !strings.Contains(msg, "not open yet") {
		t.Errorf("save-failed message = %q", msg)
	}
	h.closeStdin()
	h.expect("stopped")
	if code := h.wait(); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
	checkCleanedUp(t, home, workDir)
}

func TestServeOddPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a b%c")
	home := filepath.Join(dir, "home æ%")
	deckPath := packFixture(t, dir, "my talk æøå 100%"+brand.Ext, nil)
	h := serveDeck(t, Options{Home: home, DeckPath: deckPath})

	ev, line := h.next()
	if ev["event"] != "unpacked" || !strings.Contains(line, `"code":[]`) {
		t.Fatalf("first event = %s, want unpacked with \"code\":[]", line)
	}
	workDir := checkUnpacked(t, ev, home, deckPath, true)
	checkReady(t, h, h.expect("ready"), workDir)

	slides := filepath.Join(workDir, "slides.md")
	if err := os.WriteFile(slides, []byte("# Edited æøå 100%\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.send(`{"cmd":"save"}`)
	h.expect("saved")
	if got := entryText(t, deckPath, "slides.md"); got != "# Edited æøå 100%\n" {
		t.Errorf("the deck's slides.md = %q", got)
	}

	h.closeStdin()
	h.expect("stopped")
	if code := h.wait(); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
	checkCleanedUp(t, home, workDir)
}

func TestServeRetriesPortRace(t *testing.T) {
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, nil)
	race := errors.New("slidev exited before it was ready: exit status 1\nError: Port is already in use")
	h := serveDeck(t, Options{Home: home, DeckPath: deckPath}, race, race)
	workDir := checkUnpacked(t, h.expect("unpacked"), home, deckPath, true)
	checkReady(t, h, h.expect("ready"), workDir)
	if n := len(h.fake.configs()); n != 3 {
		t.Errorf("Start called %d times, want 3", n)
	}
	h.closeStdin()
	h.expect("stopped")
	if code := h.wait(); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
}

func TestServeGivesUpAfterThreePortRaces(t *testing.T) {
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, nil)
	race := errors.New("slidev exited before it was ready: exit status 1")
	h := serveDeck(t, Options{Home: home, DeckPath: deckPath}, race, race, race, race)
	workDir := checkUnpacked(t, h.expect("unpacked"), home, deckPath, true)
	h.expect("error")
	h.expect("stopped")
	if code := h.wait(); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if n := len(h.fake.configs()); n != 3 {
		t.Errorf("Start called %d times, want 3", n)
	}
	checkCleanedUp(t, home, workDir)
}

func TestServeUnknownCommands(t *testing.T) {
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, nil)
	h := serveDeck(t, Options{Home: home, DeckPath: deckPath})
	workDir := checkUnpacked(t, h.expect("unpacked"), home, deckPath, true)
	h.expect("ready")

	for _, line := range []string{`not json`, `{"cmd":"dance"}`, `{"answer":"maybe"}`, `{"cmd":"save","path":"x"}`, `[]`} {
		h.send(line)
		ev := h.expect("error")
		if got := str(t, ev, "message"); got != "Unknown command: "+line {
			t.Errorf("message = %q, want %q", got, "Unknown command: "+line)
		}
		if got := str(t, ev, "log"); got != "" {
			t.Errorf("log = %q, want empty", got)
		}
	}
	// An answer with no question pending is ignored: the next event is the stop's.
	h.send(`{"answer":"yes"}`)
	h.send(`{"cmd":"stop"}`)
	h.expect("stopped")
	if code := h.wait(); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
	checkCleanedUp(t, home, workDir)
}

// A signal (the context) stops it as stdin's end does.
func TestServeContextStops(t *testing.T) {
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, nil)
	h := serveDeck(t, Options{Home: home, DeckPath: deckPath})
	workDir := checkUnpacked(t, h.expect("unpacked"), home, deckPath, true)
	h.expect("ready")
	h.cancel()
	h.expect("stopped")
	if code := h.wait(); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
	checkCleanedUp(t, home, workDir)
}

func TestServeAddons(t *testing.T) {
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, nil)
	addon := filepath.Join(t.TempDir(), "addon a%b")
	h := serveDeck(t, Options{Home: home, DeckPath: deckPath, Addons: []string{addon}})
	h.expect("unpacked")
	h.expect("ready")
	wantArgs, wantEnv, err := slidev.AddonLaunch(home, []string{addon})
	if err != nil {
		t.Fatal(err)
	}
	c := h.fake.configs()[0]
	if !reflect.DeepEqual(c.NodeArgs, wantArgs) || !reflect.DeepEqual(c.Env, wantEnv) {
		t.Errorf("Start got NodeArgs %q, Env %q; want %q, %q", c.NodeArgs, c.Env, wantArgs, wantEnv)
	}
	h.closeStdin()
	h.expect("stopped")
	h.wait()
}

func TestServeMissingDeck(t *testing.T) {
	home := t.TempDir()
	h := serveDeck(t, Options{Home: home, DeckPath: filepath.Join(t.TempDir(), "gone"+brand.Ext)})
	if msg := str(t, h.expect("error"), "message"); !strings.Contains(msg, "gone"+brand.Ext) {
		t.Errorf("error message = %q, want it to name the file", msg)
	}
	h.expect("stopped")
	if code := h.wait(); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if recs := instance.List(home); len(recs) > 0 {
		t.Errorf("instance records left: %+v", recs)
	}
}

// Events are one JSON object per line, which an editor can read as they come.
func TestServeEventsAreLines(t *testing.T) {
	var out bytes.Buffer
	e := &emitter{w: &out}
	e.emit(confirmEvent{Event: "confirm", Question: "a <b> & \"c\"\nd"})
	got := out.String()
	if strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, "\n") {
		t.Fatalf("event = %q, want one line", got)
	}
	if want := `{"event":"confirm","question":"a <b> & \"c\"\nd"}` + "\n"; got != want {
		t.Errorf("event = %s, want %s", got, want)
	}
}
