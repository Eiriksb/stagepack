package export

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"stagepack/internal/app"
	"stagepack/internal/brand"
	"stagepack/internal/instance"
	"stagepack/internal/pack"
	"stagepack/internal/slidev"
	"stagepack/internal/trust"
	"stagepack/internal/ui"
	slidevrt "stagepack/runtime"
)

const fixture = "../../testdata/deck"

// packDeck packs the fixture into dir/name, with a vite.config.ts added when code is set.
func packDeck(t *testing.T, dir, name string, code bool) string {
	t.Helper()
	src := fixture
	if code {
		src = filepath.Join(t.TempDir(), "talk")
		if err := os.CopyFS(src, os.DirFS(fixture)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, "vite.config.ts"), []byte("export default {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(dir, name)
	if _, err := pack.Pack(pack.Options{Dir: src, Out: out, Warn: io.Discard}); err != nil {
		t.Fatal(err)
	}
	return out
}

// useHeadmatter makes the export see this theme and these addons (JSON) in
// every talk's headmatter, instead of running Slidev's parser.
func useHeadmatter(t *testing.T, theme, addons string) {
	t.Helper()
	old := readHeadmatter
	readHeadmatter = func(ctx context.Context, node, runtimeDir, slidesPath string) (slidev.Headmatter, error) {
		return slidev.Headmatter{Theme: json.RawMessage(theme), Addons: json.RawMessage(addons)}, nil
	}
	t.Cleanup(func() { readHeadmatter = old })
}

// syncBuffer is a bytes.Buffer safe for use from several goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) lines(t *testing.T) []map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(s.b.Bytes()))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("headless line %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

// fakeBox is a progress box the test can cancel.
type fakeBox struct {
	s         *script
	cancelled chan struct{}
	once      sync.Once
}

func (b *fakeBox) Update(step string, fraction float64) { b.s.event("step " + step) }
func (b *fakeBox) Cancelled() <-chan struct{}           { return b.cancelled }
func (b *fakeBox) Close()                               { b.once.Do(func() { b.s.event("close") }) }
func (b *fakeBox) cancel()                              { close(b.cancelled) }

// script is HeadlessDialogs with answers the test sets, and a record of what
// was shown. Updates, Error and Finished also go to the headless lines.
type script struct {
	Dialogs
	out *syncBuffer

	mu        sync.Mutex
	answers   []bool // Confirm's answers in order; no once they run out
	questions []string
	suggested []string // what AskSavePath was asked with
	save      string   // AskSavePath's answer; "" cancels
	errs      []string
	finished  []string // title + "\n" + message
	buttons   [][]string
	button    int    // Finished's answer
	onFinish  func() // runs as Finished is called
	events    []string
	boxes     []*fakeBox
	fake      bool // ShowProgress gives a fakeBox, not a headless one
	boxUp     bool
}

func newScript() *script {
	out := &syncBuffer{}
	return &script{Dialogs: HeadlessDialogs(out, false), out: out, button: -1}
}

func (s *script) event(e string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	if e == "close" {
		s.boxUp = false
	}
}

func (s *script) Confirm(title, msg string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.boxUp {
		s.events = append(s.events, "confirm with the box up")
	}
	s.events = append(s.events, "confirm")
	s.questions = append(s.questions, msg)
	if len(s.answers) == 0 {
		return false
	}
	a := s.answers[0]
	s.answers = s.answers[1:]
	return a
}

func (s *script) AskSavePath(suggest string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.suggested = append(s.suggested, suggest)
	return s.save, s.save != ""
}

func (s *script) Error(title, msg string) {
	s.mu.Lock()
	s.errs = append(s.errs, msg)
	s.mu.Unlock()
	s.Dialogs.Error(title, msg)
}

func (s *script) ShowProgress(title, head string) ui.Progress {
	s.mu.Lock()
	s.events = append(s.events, "box "+head)
	s.boxUp = true
	fake := s.fake
	s.mu.Unlock()
	if !fake {
		return &trackedBox{s.Dialogs.ShowProgress(title, head), s}
	}
	b := &fakeBox{s: s, cancelled: make(chan struct{})}
	s.mu.Lock()
	s.boxes = append(s.boxes, b)
	s.mu.Unlock()
	return b
}

func (s *script) Finished(title, msg string, buttons []string) int {
	s.mu.Lock()
	s.finished = append(s.finished, title+"\n"+msg)
	s.buttons = append(s.buttons, buttons)
	onFinish := s.onFinish
	s.mu.Unlock()
	if onFinish != nil {
		onFinish()
	}
	s.Dialogs.Finished(title, msg, buttons)
	return s.button
}

func (s *script) box(t *testing.T) *fakeBox {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.boxes) == 0 {
		t.Fatal("no progress box was shown")
	}
	return s.boxes[len(s.boxes)-1]
}

// trackedBox is a headless box whose Close the script records.
type trackedBox struct {
	ui.Progress
	s *script
}

func (b *trackedBox) Close() { b.s.event("close"); b.Progress.Close() }

// fakeRun stands in for Run: it records its calls and plays a script.
type fakeRun struct {
	mu      sync.Mutex
	cmds    []*exec.Cmd
	outs    []string
	play    func(ctx context.Context, out string, emit func(Event)) error
	started chan struct{} // gets a value when a call starts playing
}

func newRun(play func(ctx context.Context, out string, emit func(Event)) error) *fakeRun {
	return &fakeRun{play: play, started: make(chan struct{}, 8)}
}

func (f *fakeRun) run(ctx context.Context, cmd *exec.Cmd, out string, onEvent func(Event)) error {
	f.mu.Lock()
	f.cmds = append(f.cmds, cmd)
	f.outs = append(f.outs, out)
	f.mu.Unlock()
	f.started <- struct{}{}
	return f.play(ctx, out, onEvent)
}

func (f *fakeRun) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.cmds)
}

func (f *fakeRun) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-f.started:
	case <-time.After(10 * time.Second):
		t.Fatal("Run was not called")
	}
}

const mb = 1 << 20

// finishes plays a whole export: the exporter's events, the .partial file
// written and renamed into place, then done.
func finishes(clips int, notices ...string) func(context.Context, string, func(Event)) error {
	return func(ctx context.Context, out string, emit func(Event)) error {
		emit(Event{Event: "start", Slides: 2})
		for n := 1; n <= 2; n++ {
			emit(Event{Event: "slide", Slide: n, States: 1})
			emit(Event{Event: "state", Slide: n, State: 0})
		}
		for _, text := range notices {
			emit(Event{Event: "notice", Text: text})
		}
		emit(Event{Event: "write", Path: out})
		if err := os.WriteFile(out+".partial", []byte("pptx"), 0o644); err != nil {
			return err
		}
		if err := os.Rename(out+".partial", out); err != nil {
			return err
		}
		emit(Event{Event: "done", Path: out, Slides: 2, Clips: clips, Bytes: 3 * mb / 2})
		return nil
	}
}

// blocks writes the .partial file (and renames it into place when renamed is
// set, as the exporter does just before done), then waits to be cancelled.
// It leaves .partial behind, as a Run that was killed could.
func blocks(renamed bool) func(context.Context, string, func(Event)) error {
	return func(ctx context.Context, out string, emit func(Event)) error {
		emit(Event{Event: "start", Slides: 2})
		if err := os.WriteFile(out+".partial", []byte("new"), 0o644); err != nil {
			return err
		}
		if renamed {
			if err := os.Rename(out+".partial", out); err != nil {
				return err
			}
		}
		<-ctx.Done()
		return ctx.Err()
	}
}

// fails writes the .partial file and fails with msg.
func fails(msg string) func(context.Context, string, func(Event)) error {
	return func(ctx context.Context, out string, emit func(Event)) error {
		emit(Event{Event: "start", Slides: 2})
		if err := os.WriteFile(out+".partial", []byte("half"), 0o644); err != nil {
			return err
		}
		return errors.New(msg)
	}
}

// harness is a home folder, a folder with talk.sptx in it, scripted dialogs
// and a fake Run.
type harness struct {
	home, dir, deck string
	d               *script
	run             *fakeRun
	opened          []string
	revealed        []string
	mu              sync.Mutex
}

func newHarness(t *testing.T, play func(context.Context, string, func(Event)) error) *harness {
	t.Helper()
	useHeadmatter(t, `"default"`, `[]`)
	dir := t.TempDir()
	return &harness{home: t.TempDir(), dir: dir, deck: packDeck(t, dir, "talk"+brand.Ext, false), d: newScript(), run: newRun(play)}
}

func (h *harness) options(animations bool) Options {
	return Options{
		Home: h.home, DeckPath: h.deck, Animations: animations, Dialogs: h.d, GOOS: runtime.GOOS, Run: h.run.run,
		FindBrowser: func() (string, bool) { return "/usr/bin/fake-chrome", true },
		Open: func(p string) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.opened = append(h.opened, p)
			return nil
		},
		Reveal: func(p string) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.revealed = append(h.revealed, p)
			return nil
		},
	}
}

func (h *harness) workDir() string {
	return filepath.Join(h.home, "runtimes", strconv.Itoa(slidevrt.Major()), "work")
}

func (h *harness) lockPath(deck string) string {
	return filepath.Join(h.home, "instances", instance.Key(deck)+".export")
}

// checkNothingLeft fails unless the work folder and the lock are gone, and out
// and its .partial do not exist.
func (h *harness) checkNothingLeft(t *testing.T, out string) {
	t.Helper()
	h.checkCleanedUp(t)
	for _, p := range []string{out, out + ".partial"} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s left behind", p)
		}
	}
}

// checkCleanedUp fails unless the work folder and the lock are gone.
func (h *harness) checkCleanedUp(t *testing.T) {
	t.Helper()
	if ents, _ := os.ReadDir(h.workDir()); len(ents) != 0 {
		t.Errorf("work folders left behind: %v", ents)
	}
	if _, err := os.Stat(h.lockPath(h.deck)); err == nil {
		t.Errorf("lock %s left behind", h.lockPath(h.deck))
	}
}

// wait is what an Export running in the background returned.
func wait(t *testing.T, errc <-chan error) error {
	t.Helper()
	select {
	case err := <-errc:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Export did not return")
		return nil
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestExportWritesNextToTheFile(t *testing.T) {
	for _, c := range []struct {
		animations bool
		name       string
		clips      int
		button     int
		msg        string
	}{
		{false, "talk.pptx", 0, 0, "2 slides, 0 with a video, 1.5 MB"},
		{true, "talk-animations.pptx", 1, 1, "2 slides, 1 with a video, 1.5 MB"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, finishes(c.clips))
			h.d.button = c.button
			h.d.onFinish = func() { h.checkCleanedUp(t) } // the box may stay up a long time
			res, err := Export(context.Background(), h.options(c.animations))
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(h.dir, c.name)
			if res.Path != out || res.Slides != 2 || res.Clips != c.clips || res.Bytes != 3*mb/2 || len(res.Notes) != 0 {
				t.Errorf("result %+v", res)
			}
			if got := readFile(t, out); got != "pptx" {
				t.Errorf("%s holds %q", out, got)
			}
			if h.run.calls() != 1 {
				t.Fatalf("Run called %d times", h.run.calls())
			}
			cmd := h.run.cmds[0]
			if h.run.outs[0] != out || !slices.Contains(cmd.Args, out) || slices.Contains(cmd.Args, "--animations") != c.animations {
				t.Errorf("out %q, args %q", h.run.outs[0], cmd.Args)
			}
			if slides := cmd.Args[2]; !strings.HasPrefix(slides, h.workDir()+string(filepath.Separator)) || filepath.Base(slides) != "slides.md" {
				t.Errorf("slides file %q, want slides.md in a work folder", slides)
			}
			if want := []string{"Exported " + c.name + "\n" + c.msg}; !slices.Equal(h.d.finished, want) {
				t.Errorf("finished %q, want %q", h.d.finished, want)
			}
			if len(h.d.buttons) != 1 || !slices.Equal(h.d.buttons[0], []string{"Open", "Show in folder", "Close"}) {
				t.Errorf("buttons %q", h.d.buttons)
			}
			if c.button == 0 && (!slices.Equal(h.opened, []string{out}) || len(h.revealed) != 0) {
				t.Errorf("Open pressed: opened %q, revealed %q", h.opened, h.revealed)
			}
			if c.button == 1 && (!slices.Equal(h.revealed, []string{out}) || len(h.opened) != 0) {
				t.Errorf("Show in folder pressed: opened %q, revealed %q", h.opened, h.revealed)
			}
			if len(h.d.errs) != 0 || len(h.d.questions) != 0 {
				t.Errorf("errors %q, questions %q", h.d.errs, h.d.questions)
			}
			h.checkCleanedUp(t)

			// The headless box: the head, then each step, and the finished line.
			var steps []string
			var finished map[string]any
			for _, l := range h.d.out.lines(t) {
				switch l["event"] {
				case "progress":
					steps = append(steps, l["step"].(string))
				case "finished":
					finished = l
				}
			}
			kind := "editable"
			if c.animations {
				kind = "with animations"
			}
			if want := "box talk" + brand.Ext + ", " + kind; h.d.events[0] != want {
				t.Errorf("first event %q, want %q", h.d.events[0], want)
			}
			for _, want := range []string{"Unpacking", "Starting Slidev", "Slide 2 of 2", "Writing the file"} {
				if !slices.Contains(steps, want) {
					t.Errorf("steps %q lack %q", steps, want)
				}
			}
			if finished == nil || finished["title"] != "Exported "+c.name || finished["message"] != c.msg {
				t.Errorf("finished line %v", finished)
			}
		})
	}
}

func TestExportAsksBeforeReplacing(t *testing.T) {
	h := newHarness(t, finishes(0))
	out := filepath.Join(h.dir, "talk.pptx")
	if err := os.WriteFile(out, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	h.d.answers = []bool{false}
	if _, err := Export(context.Background(), h.options(false)); !errors.Is(err, ErrCancelled) {
		t.Fatalf("err = %v, want ErrCancelled", err)
	}
	if want := []string{"Replace talk.pptx?"}; !slices.Equal(h.d.questions, want) {
		t.Errorf("questions %q, want %q", h.d.questions, want)
	}
	if h.run.calls() != 0 {
		t.Errorf("Run called %d times after No", h.run.calls())
	}
	if got := readFile(t, out); got != "old" {
		t.Errorf("the old file holds %q", got)
	}
	h.checkCleanedUp(t)

	h.d.answers = []bool{true}
	if _, err := Export(context.Background(), h.options(false)); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, out); got != "pptx" {
		t.Errorf("after Yes the file holds %q", got)
	}

	// Replace replaces without asking.
	o := h.options(false)
	o.Replace = true
	if _, err := Export(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if len(h.d.questions) != 2 || h.run.calls() != 2 {
		t.Errorf("questions %q, Run called %d times", h.d.questions, h.run.calls())
	}
}

func TestExportAsksWhereWhenTheFolderCantBeWritten(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a folder's mode doesn't stop writing on Windows")
	}
	h := newHarness(t, finishes(0))
	if err := os.Chmod(h.dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(h.dir, 0o755) })
	if f, err := os.CreateTemp(h.dir, "probe"); err == nil {
		f.Close()
		os.Remove(f.Name())
		t.Skip("this user can write into a read-only folder (root)")
	}
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)

	// Cancelled: nothing run.
	if _, err := Export(context.Background(), h.options(false)); !errors.Is(err, ErrCancelled) {
		t.Fatalf("err = %v, want ErrCancelled", err)
	}
	if want := []string{filepath.Join(userHome, "talk.pptx")}; !slices.Equal(h.d.suggested, want) {
		t.Errorf("suggested %q, want %q", h.d.suggested, want)
	}
	if h.run.calls() != 0 {
		t.Errorf("Run called %d times", h.run.calls())
	}
	h.checkCleanedUp(t)

	// A place chosen: the file goes there, named .pptx.
	elsewhere := t.TempDir()
	h.d.save = filepath.Join(elsewhere, "mine")
	res, err := Export(context.Background(), h.options(false))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(elsewhere, "mine.pptx")
	if res.Path != want || readFile(t, want) != "pptx" {
		t.Errorf("result %+v, want the file in %s", res, want)
	}
	if len(h.d.finished) != 1 || !strings.HasPrefix(h.d.finished[0], "Exported mine.pptx\n") {
		t.Errorf("finished %q", h.d.finished)
	}
	if ents, _ := os.ReadDir(h.dir); len(ents) != 1 {
		t.Errorf("the read-only folder holds %v", ents)
	}

	// The name chosen without .pptx: the file with it is asked about.
	if _, err := Export(context.Background(), h.options(false)); !errors.Is(err, ErrCancelled) {
		t.Errorf("err = %v, want ErrCancelled", err)
	}
	if n := len(h.d.questions); n != 1 || h.d.questions[0] != "Replace mine.pptx?" {
		t.Errorf("questions %q", h.d.questions)
	}
	if h.run.calls() != 1 {
		t.Errorf("Run called %d times", h.run.calls())
	}
}

func TestExportAsksTheCodeQuestionOnce(t *testing.T) {
	h := newHarness(t, finishes(0))
	h.deck = packDeck(t, h.dir, "talk"+brand.Ext, true)
	out := filepath.Join(h.dir, "talk.pptx")

	// No: nothing run, nothing left.
	_, err := Export(context.Background(), h.options(false))
	if !errors.Is(err, ErrNotTrusted) || !errors.Is(err, app.ErrNotTrusted) {
		t.Fatalf("err = %v, want ErrNotTrusted", err)
	}
	if len(h.d.questions) != 1 {
		t.Fatalf("asked %d times", len(h.d.questions))
	}
	for _, want := range []string{`"Fixture talk"`, "talk" + brand.Ext, "contains code", "vite.config.ts"} {
		if !strings.Contains(h.d.questions[0], want) {
			t.Errorf("question lacks %q:\n%s", want, h.d.questions[0])
		}
	}
	if h.run.calls() != 0 {
		t.Errorf("Run called %d times after No", h.run.calls())
	}
	h.checkNothingLeft(t, out)
	if _, err := os.Stat(filepath.Join(h.home, trust.FileName)); err == nil {
		t.Error("a No was remembered")
	}

	// Yes: asked with the box away, run, and remembered.
	h.d.answers = []bool{true}
	if _, err := Export(context.Background(), h.options(false)); err != nil {
		t.Fatal(err)
	}
	if len(h.d.questions) != 2 || h.run.calls() != 1 {
		t.Fatalf("asked %d times, Run called %d times", len(h.d.questions), h.run.calls())
	}
	if ev := strings.Join(h.d.events, ","); strings.Contains(ev, "with the box up") {
		t.Errorf("asked with the progress box up: %s", ev)
	}
	if _, err := os.Stat(filepath.Join(h.home, trust.FileName)); err != nil {
		t.Errorf("the Yes was not remembered: %v", err)
	}

	// Again: not asked (the script would say no).
	if _, err := Export(context.Background(), h.options(true)); err != nil {
		t.Fatal(err)
	}
	if len(h.d.questions) != 2 || h.run.calls() != 2 {
		t.Errorf("asked %d times, Run called %d times", len(h.d.questions), h.run.calls())
	}

	// TrustCode runs it without asking.
	o := h.options(false)
	o.Home = t.TempDir()
	o.TrustCode, o.Replace = true, true
	if _, err := Export(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if len(h.d.questions) != 2 {
		t.Errorf("asked with TrustCode: %q", h.d.questions[2:])
	}
}

func TestExportAsksAboutCodeInTheHeadmatter(t *testing.T) {
	h := newHarness(t, finishes(0))
	useHeadmatter(t, `"./my-theme"`, `[]`) // a theme from the talk's own folder, as opening sees it
	if _, err := Export(context.Background(), h.options(false)); !errors.Is(err, ErrNotTrusted) {
		t.Fatalf("err = %v, want ErrNotTrusted", err)
	}
	if len(h.d.questions) != 1 || !strings.Contains(h.d.questions[0], "my-theme") {
		t.Errorf("questions %q", h.d.questions)
	}
	if h.run.calls() != 0 {
		t.Errorf("Run called %d times", h.run.calls())
	}
}

func TestExportCancelLeavesNothing(t *testing.T) {
	h := newHarness(t, blocks(false))
	h.d.fake = true
	errc := make(chan error, 1)
	go func() { _, err := Export(context.Background(), h.options(true)); errc <- err }()
	h.run.waitStarted(t)
	h.d.box(t).cancel()
	if err := wait(t, errc); !errors.Is(err, ErrCancelled) {
		t.Errorf("err = %v, want ErrCancelled", err)
	}
	h.checkNothingLeft(t, filepath.Join(h.dir, "talk-animations.pptx"))
	if len(h.d.errs) != 0 || len(h.d.finished) != 0 {
		t.Errorf("errors %q, finished %q after Cancel", h.d.errs, h.d.finished)
	}
	if ev := h.d.events; ev[len(ev)-1] != "close" {
		t.Errorf("the box was not closed: %q", ev)
	}
}

func TestExportCancelAfterTheFileWasRenamedRemovesIt(t *testing.T) {
	h := newHarness(t, blocks(true))
	h.d.fake = true
	errc := make(chan error, 1)
	go func() { _, err := Export(context.Background(), h.options(false)); errc <- err }()
	h.run.waitStarted(t)
	out := filepath.Join(h.dir, "talk.pptx")
	h.d.box(t).cancel()
	if err := wait(t, errc); !errors.Is(err, ErrCancelled) {
		t.Errorf("err = %v, want ErrCancelled", err)
	}
	h.checkNothingLeft(t, out)
}

func TestExportCancelKeepsTheFileThatWasThere(t *testing.T) {
	h := newHarness(t, blocks(false))
	h.d.fake = true
	out := filepath.Join(h.dir, "talk.pptx")
	if err := os.WriteFile(out, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	hourAgo := time.Now().Add(-time.Hour)
	if err := os.Chtimes(out, hourAgo, hourAgo); err != nil {
		t.Fatal(err)
	}
	o := h.options(false)
	o.Replace = true
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { _, err := Export(ctx, o); errc <- err }()
	h.run.waitStarted(t)
	cancel() // as from Ctrl-C
	if err := wait(t, errc); !errors.Is(err, ErrCancelled) {
		t.Errorf("err = %v, want ErrCancelled", err)
	}
	if got := readFile(t, out); got != "old" {
		t.Errorf("the file that was there holds %q", got)
	}
	if _, err := os.Stat(out + ".partial"); err == nil {
		t.Error(".partial left behind")
	}
	h.checkCleanedUp(t)
}

func TestExportFailureIsOneSentence(t *testing.T) {
	h := newHarness(t, fails("Slidev could not start.\n    at start (slidev.mjs:12)"))
	_, err := Export(context.Background(), h.options(false))
	if err == nil || errors.Is(err, ErrCancelled) {
		t.Fatalf("err = %v, want a failure", err)
	}
	want := "Couldn't export talk" + brand.Ext + ": Slidev could not start. The log is in " + filepath.Join(h.home, "logs") + "."
	if !slices.Equal(h.d.errs, []string{want}) {
		t.Errorf("errors %q, want %q", h.d.errs, want)
	}
	if len(h.d.finished) != 0 {
		t.Errorf("finished %q after a failure", h.d.finished)
	}
	h.checkNothingLeft(t, filepath.Join(h.dir, "talk.pptx"))
	// The exporter's own output goes to the log folder.
	if f, ok := h.run.cmds[0].Stderr.(*os.File); !ok || filepath.Dir(f.Name()) != filepath.Join(h.home, "logs") {
		t.Errorf("the exporter's stderr is %v, want a file in the log folder", h.run.cmds[0].Stderr)
	}
}

func TestExportFailureSaysTheFileMayBeOpenInPowerPoint(t *testing.T) {
	for _, msg := range []string{
		"EBUSY: resource busy or locked, rename 'talk.pptx.partial' -> 'talk.pptx'",
		"EPERM: operation not permitted, rename 'talk.pptx.partial' -> 'talk.pptx'",
		"The process cannot access the file because it is being used by another process.",
	} {
		h := newHarness(t, fails(msg))
		if _, err := Export(context.Background(), h.options(false)); err == nil {
			t.Fatal("no error")
		}
		want := "Couldn't export talk" + brand.Ext + ": talk.pptx may be open in PowerPoint; close it and try again."
		if !slices.Equal(h.d.errs, []string{want}) {
			t.Errorf("%s: errors %q, want %q", msg, h.d.errs, want)
		}
	}
}

func TestExportSameFileTwiceIsRefused(t *testing.T) {
	h := newHarness(t, blocks(false))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { _, err := Export(ctx, h.options(false)); errc <- err }()
	h.run.waitStarted(t)
	got, ok := instance.ParseExportLock(readFile(t, h.lockPath(h.deck)))
	if !ok || got.PID != os.Getpid() {
		t.Errorf("the lock holds %+v, want this PID", got)
	}
	// and the work folder, so that opening a talk meanwhile keeps it
	if rel, err := filepath.Rel(h.workDir(), got.WorkDir); err != nil || strings.Contains(rel, string(filepath.Separator)) || rel == "." || strings.HasPrefix(rel, "..") {
		t.Errorf("the lock names work folder %q, want one under %s", got.WorkDir, h.workDir())
	}

	second := newScript()
	o := h.options(true) // the other kind of export of the same file
	o.Dialogs = second
	if _, err := Export(context.Background(), o); !errors.Is(err, ErrBusy) {
		t.Errorf("err = %v, want ErrBusy", err)
	}
	if want := []string{"Already exporting talk" + brand.Ext + "."}; !slices.Equal(second.errs, want) {
		t.Errorf("errors %q, want %q", second.errs, want)
	}
	if h.run.calls() != 1 {
		t.Errorf("Run called %d times", h.run.calls())
	}

	cancel()
	if err := wait(t, errc); !errors.Is(err, ErrCancelled) {
		t.Errorf("first export: %v", err)
	}
	h.checkCleanedUp(t)
	h.run.play = finishes(0)
	if _, err := Export(context.Background(), h.options(false)); err != nil {
		t.Errorf("after the first ended: %v", err)
	}
}

func TestExportDifferentFilesTogether(t *testing.T) {
	var (
		mu      sync.Mutex
		started int
		both    = make(chan struct{})
	)
	whenBoth := func(ctx context.Context, out string, emit func(Event)) error {
		mu.Lock()
		if started++; started == 2 {
			close(both)
		}
		mu.Unlock()
		select {
		case <-both:
		case <-time.After(10 * time.Second):
			return errors.New("the other export never started")
		}
		return finishes(0)(ctx, out, emit)
	}
	h := newHarness(t, whenBoth)
	other := packDeck(t, h.dir, "other"+brand.Ext, false)
	errc := make(chan error, 2)
	for _, deck := range []string{h.deck, other} {
		o := h.options(false)
		o.DeckPath = deck
		o.Dialogs = newScript()
		go func() { _, err := Export(context.Background(), o); errc <- err }()
	}
	for range 2 {
		if err := wait(t, errc); err != nil {
			t.Error(err)
		}
	}
	for _, name := range []string{"talk.pptx", "other.pptx"} {
		if _, err := os.Stat(filepath.Join(h.dir, name)); err != nil {
			t.Error(err)
		}
	}
	h.checkCleanedUp(t)
}

func TestExportTakesOverALockFromAKilledExport(t *testing.T) {
	h := newHarness(t, finishes(0))
	// A PID that ran and has exited.
	gone := exec.Command(os.Args[0], "-test.run=^$")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(h.home, "instances"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.lockPath(h.deck), []byte(strconv.Itoa(gone.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Export(context.Background(), h.options(false)); err != nil {
		t.Fatalf("a dead export's lock was not taken over: %v", err)
	}
	h.checkCleanedUp(t)

	// A lock whose PID runs is not.
	if err := os.WriteFile(h.lockPath(h.deck), []byte(strconv.Itoa(os.Getppid())), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Export(context.Background(), h.options(false)); !errors.Is(err, ErrBusy) {
		t.Errorf("err = %v, want ErrBusy", err)
	}
	if h.run.calls() != 1 {
		t.Errorf("Run called %d times", h.run.calls())
	}
}

func TestExportNotes(t *testing.T) {
	others := []string{
		"Notice: slide 1, click 2: the clip's last frame differs from a fresh load of the slide (the page's live state differs from a fresh load, or a recording is wrong).",
		"Warning: slide 2 is still too full after shrinking to 55% - content may run off the slide.",
		`Notice: font "Inter" not embedded: no installed font of that family.`,
		`Notice: slide 3: transition "zoom" has no form in PowerPoint - left out.`,
	}
	h := newHarness(t, finishes(1,
		"Notice: slide 1, click 1: clip, 1.5 s, 2.1 MB.",
		others[0],
		"Notice: slide 2: chart exported as a picture (it is drawn on a canvas).",
		others[1],
		"Notice: slide 1, click 2: clip, 0.8 s, 0.4 MB.",
		others[2],
		"Notice: slide 3: diagram exported as a picture (an SVG filter).",
		others[3],
	))
	res, err := Export(context.Background(), h.options(true))
	if err != nil {
		t.Fatal(err)
	}
	want := append(others[:3:3], "2 parts are pictures")
	if !slices.Equal(res.Notes, want) {
		t.Errorf("notes %q, want %q", res.Notes, want)
	}
	msg := "2 slides, 1 with a video, 1.5 MB\n\n" + strings.Join(want, "\n")
	if wantBox := []string{"Exported talk-animations.pptx\n" + msg}; !slices.Equal(h.d.finished, wantBox) {
		t.Errorf("finished %q, want %q", h.d.finished, wantBox)
	}

	if got := notes([]string{"Notice: slide 2: chart exported as a picture (canvas)."}); !slices.Equal(got, []string{"1 part is a picture"}) {
		t.Errorf("one picture: %q", got)
	}
}

func TestExportPathWithSpacesAndQuotes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip(`a file name can't hold " on Windows`)
	}
	h := newHarness(t, finishes(0))
	dir := filepath.Join(t.TempDir(), "my talks")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	h.dir = dir
	h.deck = packDeck(t, dir, `a "b" $c ø`+brand.Ext, false)
	res, err := Export(context.Background(), h.options(false))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, `a "b" $c ø.pptx`)
	if res.Path != want || readFile(t, want) != "pptx" {
		t.Errorf("result %+v, want %s", res, want)
	}
	if !slices.Contains(h.run.cmds[0].Args, want) {
		t.Errorf("args %q lack %q", h.run.cmds[0].Args, want)
	}
	if len(h.d.finished) != 1 || !strings.HasPrefix(h.d.finished[0], `Exported a "b" $c ø.pptx`+"\n") {
		t.Errorf("finished %q", h.d.finished)
	}
	h.checkCleanedUp(t)
}

func TestHeadlessDialogs(t *testing.T) {
	var buf syncBuffer
	d := HeadlessDialogs(&buf, false)
	if d.Confirm("Stagepack", "Replace talk.pptx?") {
		t.Error("headless Confirm said yes")
	}
	free := filepath.Join(t.TempDir(), "talk.pptx")
	if p, ok := d.AskSavePath(free); p != free || !ok {
		t.Errorf("AskSavePath(a new file) = %q, %v", p, ok)
	}
	taken := filepath.Join(t.TempDir(), "taken.pptx")
	if err := os.WriteFile(taken, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if p, ok := d.AskSavePath(taken); ok {
		t.Errorf("AskSavePath(an existing file) = %q, %v; nobody agreed to replace it", p, ok)
	}
	if p, ok := HeadlessDialogs(io.Discard, true).AskSavePath(taken); p != taken || !ok {
		t.Errorf("AskSavePath(an existing file) when made to replace = %q, %v", p, ok)
	}
	d.Error("Stagepack", "Couldn't export talk.sptx: no.")
	box := d.ShowProgress("Stagepack", "talk.sptx, editable")
	box.Update("Unpacking", 0)
	box.Close()
	if got := d.Finished("Exported talk.pptx", "2 slides", []string{"Open", "Close"}); got != -1 {
		t.Errorf("Finished = %d", got)
	}
	str := func(l map[string]any, k string) string { v, _ := l[k].(string); return v }
	var got []string
	for _, l := range buf.lines(t) {
		got = append(got, strings.Join([]string{str(l, "event"), str(l, "title"), str(l, "message"), str(l, "path"), str(l, "step"), str(l, "answer")}, "|"))
	}
	want := []string{
		"confirm|Stagepack|Replace talk.pptx?|||no",
		"save|||" + free + "||",
		"save|||" + taken + "||no",
		"error|Stagepack|Couldn't export talk.sptx: no.|||",
		"progress||||Unpacking|",
		"finished|Exported talk.pptx|2 slides|||",
	}
	if !slices.Equal(got, want) {
		t.Errorf("lines\n%q\nwant\n%q", got, want)
	}
}

func TestExportHeadlessDoesNotReplaceAFileNobodyAgreedTo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a folder's mode doesn't stop writing on Windows")
	}
	h := newHarness(t, finishes(0))
	if err := os.Chmod(h.dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(h.dir, 0o755) })
	if f, err := os.CreateTemp(h.dir, "probe"); err == nil {
		f.Close()
		os.Remove(f.Name())
		t.Skip("this user can write into a read-only folder (root)")
	}
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	there := filepath.Join(userHome, "talk.pptx")
	if err := os.WriteFile(there, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	o := h.options(false)
	o.Dialogs = HeadlessDialogs(io.Discard, false)
	if _, err := Export(context.Background(), o); !errors.Is(err, ErrCancelled) {
		t.Errorf("err = %v, want ErrCancelled", err)
	}
	if got := readFile(t, there); got != "old" {
		t.Errorf("%s holds %q; nobody agreed to replace it", there, got)
	}
	if h.run.calls() != 0 {
		t.Errorf("Run called %d times", h.run.calls())
	}
	h.checkCleanedUp(t)

	// Made to replace (as with --yes), it does.
	o.Dialogs, o.Replace = HeadlessDialogs(io.Discard, true), true
	if _, err := Export(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, there); got != "pptx" {
		t.Errorf("%s holds %q after an export made to replace", there, got)
	}
}

func TestExportFailureOnlyOnStderrSaysTheFileMayBeOpenInPowerPoint(t *testing.T) {
	h := newHarness(t, nil)
	t.Setenv(fakeEnv, "stderr")
	t.Setenv(fakeStderrEnv, "Error: EBUSY: resource busy or locked, rename 'talk.pptx.partial' -> 'talk.pptx'")
	o := h.options(false)
	o.Run = func(ctx context.Context, cmd *exec.Cmd, out string, onEvent func(Event)) error {
		cmd.Path = os.Args[0] // the test binary as the exporter, with the session's own stderr
		return Run(ctx, cmd, out, onEvent)
	}
	if _, err := Export(context.Background(), o); err == nil {
		t.Fatal("no error")
	}
	want := "Couldn't export talk" + brand.Ext + ": talk.pptx may be open in PowerPoint; close it and try again."
	if !slices.Equal(h.d.errs, []string{want}) {
		t.Errorf("errors %q, want %q", h.d.errs, want)
	}
	logFile := filepath.Join(h.home, "logs", "export-"+instance.Key(h.deck)+".log")
	if got := readFile(t, logFile); !strings.Contains(got, "EBUSY") {
		t.Errorf("the log lacks the exporter's stderr:\n%s", got)
	}
	h.checkNothingLeft(t, filepath.Join(h.dir, "talk.pptx"))
}

func TestExportTakesOverALockOlderThanSixHours(t *testing.T) {
	h := newHarness(t, finishes(0))
	if err := os.MkdirAll(filepath.Join(h.home, "instances"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A running PID (this one) in a fresh lock: busy.
	lock := h.lockPath(h.deck)
	if err := os.WriteFile(lock, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Export(context.Background(), h.options(false)); !errors.Is(err, ErrBusy) {
		t.Errorf("fresh lock: err = %v, want ErrBusy", err)
	}
	// The same PID in a lock 7 hours old: a reused PID, taken over.
	old := time.Now().Add(-7 * time.Hour)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := Export(context.Background(), h.options(false)); err != nil {
		t.Errorf("a 7-hour-old lock was not taken over: %v", err)
	}
	if h.run.calls() != 1 {
		t.Errorf("Run called %d times, want 1", h.run.calls())
	}
	h.checkCleanedUp(t)
}

func TestExportUsesTheBrowserItFinds(t *testing.T) {
	h := newHarness(t, finishes(0))
	o := h.options(false)
	o.FindBrowser = func() (string, bool) { return "/opt/browser/chrome", true }
	if _, err := Export(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(h.run.cmds[0].Env, "SLIDEV_PPTX_BROWSER=/opt/browser/chrome") {
		t.Errorf("env lacks the found browser: %q", h.run.cmds[0].Env)
	}
}

func TestExportWithoutABrowserSaysWhatToInstall(t *testing.T) {
	for goos, want := range map[string]string{
		"linux":   "Exporting to PowerPoint needs Chrome, Edge or Chromium. Install one and try again.",
		"windows": "Exporting to PowerPoint needs Microsoft Edge or Chrome.",
	} {
		h := newHarness(t, finishes(0))
		o := h.options(false)
		o.GOOS = goos
		o.FindBrowser = func() (string, bool) { return "", false }
		_, err := Export(context.Background(), o)
		if !errors.Is(err, ErrNoBrowser) {
			t.Fatalf("%s: err = %v, want ErrNoBrowser", goos, err)
		}
		if !slices.Equal(h.d.errs, []string{want}) {
			t.Errorf("%s: errors %q, want %q", goos, h.d.errs, want)
		}
		if h.run.calls() != 0 {
			t.Errorf("%s: Run was called", goos)
		}
		if _, err := os.Stat(h.workDir()); err == nil {
			ents, _ := os.ReadDir(h.workDir())
			if len(ents) != 0 {
				t.Errorf("%s: something was unpacked: %v", goos, ents)
			}
		}
		h.checkNothingLeft(t, filepath.Join(h.dir, "talk.pptx"))
	}
}

// tempSeen is what the Run in the Windows test saw of its TEMP folder.
type tempSeen struct {
	temp    string
	existed bool
	slides  string
}

func TestExportOnWindowsGivesTheExporterATempFolderAndRemovesIt(t *testing.T) {
	for _, c := range []struct {
		name string
		play func(context.Context, string, func(Event)) error
		stop bool
	}{
		{"done", finishes(0), false},
		{"failed", fails("boom"), false},
		{"cancelled", blocks(false), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, c.play)
			h.d.fake = true
			o := h.options(false)
			o.GOOS = "windows"
			var seen tempSeen
			inner := o.Run
			o.Run = func(ctx context.Context, cmd *exec.Cmd, out string, on func(Event)) error {
				for _, e := range cmd.Env {
					if v, ok := strings.CutPrefix(e, "TEMP="); ok {
						seen.temp = v
					}
					if v, ok := strings.CutPrefix(e, "TMP="); ok && v != seen.temp {
						t.Errorf("TMP = %q, TEMP = %q", v, seen.temp)
					}
				}
				if fi, err := os.Stat(seen.temp); seen.temp != "" && err == nil && fi.IsDir() {
					seen.existed = true
				}
				seen.slides = cmd.Args[2]
				return inner(ctx, cmd, out, on)
			}
			errc := make(chan error, 1)
			go func() { _, err := Export(context.Background(), o); errc <- err }()
			if c.stop {
				h.run.waitStarted(t)
				h.d.box(t).cancel()
			}
			wait(t, errc)
			if seen.temp == "" {
				t.Fatal("Run got no TEMP folder")
			}
			if !seen.existed {
				t.Errorf("%s did not exist while Run ran", seen.temp)
			}
			if _, err := os.Stat(seen.temp); err == nil {
				t.Errorf("%s left behind", seen.temp)
			}
			// Not under the folder Slidev serves, nor the deck's folder.
			workDir := filepath.Dir(seen.slides)
			for _, bad := range []string{workDir, h.dir} {
				if rel, err := filepath.Rel(bad, seen.temp); err == nil && !strings.HasPrefix(rel, "..") {
					t.Errorf("%s is inside %s", seen.temp, bad)
				}
			}
		})
	}
}

func TestExportOnLinuxGivesTheExporterNoTempFolder(t *testing.T) {
	h := newHarness(t, finishes(0))
	h.d.fake = true
	o := h.options(false)
	o.GOOS = "linux"
	t.Setenv("TEMP", "/orig")
	t.Setenv("TMP", "/orig")
	if _, err := Export(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	h.run.mu.Lock()
	defer h.run.mu.Unlock()
	for _, e := range h.run.cmds[0].Env {
		if (strings.HasPrefix(e, "TEMP=") || strings.HasPrefix(e, "TMP=")) && !strings.HasSuffix(e, "=/orig") {
			t.Errorf("env has %s", e)
		}
	}
}
