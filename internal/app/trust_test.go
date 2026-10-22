package app

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stagepack/internal/brand"
	"stagepack/internal/instance"
	"stagepack/internal/pack"
	"stagepack/internal/trust"
)

// packCodeFixture packs the fixture with a vite.config.ts added: a talk that contains code.
func packCodeFixture(t *testing.T, dir string) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "talk")
	if err := os.CopyFS(src, os.DirFS(fixture)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "vite.config.ts"), []byte("export default {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "talk"+brand.Ext)
	if _, err := pack.Pack(pack.Options{Dir: src, Out: out, Warn: io.Discard}); err != nil {
		t.Fatal(err)
	}
	return out
}

// replaceEntry rewrites the deck with entry name holding data, the rest (manifest included) unchanged.
func replaceEntry(t *testing.T, deckPath, name, data string) {
	t.Helper()
	zr, err := zip.OpenReader(deckPath)
	if err != nil {
		t.Fatal(err)
	}
	tmp := deckPath + ".new"
	f, err := os.Create(tmp)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, e := range zr.File {
		w, err := zw.Create(e.Name)
		if err != nil {
			t.Fatal(err)
		}
		if e.Name == name {
			io.WriteString(w, data)
			continue
		}
		rc, err := e.Open()
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(w, rc)
		rc.Close()
	}
	zr.Close()
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.Rename(tmp, deckPath); err != nil {
		t.Fatal(err)
	}
}

// openAndStop opens the deck with a fake UI that answers Confirm with answer,
// stops it once it runs if it is expected to run, and returns the UI and Open's result.
func openAndStop(t *testing.T, o Options, answer, runs bool) (*fakeUI, error) {
	t.Helper()
	fu := newFakeUI()
	fu.answer = answer
	o.UI, o.Browser, o.GOOS, o.Env = fu.factory, (&browserLog{}).open, "linux", os.Getenv
	res := openAsync(o)
	if runs {
		fu.waitRunning(t)
		fu.acts.Stop()
	}
	return fu, waitResult(t, res)
}

func TestCodeDeckAskedOnceThenRemembered(t *testing.T) {
	fs := useFakeSlidev(t)
	home := t.TempDir()
	deckPath := packCodeFixture(t, t.TempDir())

	fu, err := openAndStop(t, Options{Home: home, DeckPath: deckPath}, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(fu.asked) != 1 {
		t.Fatalf("asked %d times, want once", len(fu.asked))
	}
	for _, want := range []string{`"Fixture talk"`, "talk" + brand.Ext, "vite.config.ts", "Yes", "No"} {
		if !strings.Contains(fu.asked[0], want) {
			t.Errorf("question lacks %q:\n%s", want, fu.asked[0])
		}
	}
	if strings.Contains(strings.Join(fu.events, ","), "with the Opening box up") {
		t.Errorf("asked while the Opening box was up: %q", fu.events)
	}
	// The Opening box is closed for the question and shown again while Slidev starts.
	if got := strings.Join(fu.events, ","); got != "opening talk,opened,confirm,opening talk,opened" {
		t.Errorf("events = %s", got)
	}
	if n := len(fs.configs()); n != 1 {
		t.Fatalf("Slidev started %d times, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(home, trust.FileName)); err != nil {
		t.Errorf("the yes was not remembered: %v", err)
	}

	// The same talk again, its notes edited: not asked (the fake would say no).
	fs.mu.Lock()
	fs.srv = newFakeServer(fs.srv.base) // the first one has stopped
	fs.mu.Unlock()
	before := entryText(t, deckPath, "slides.md")
	replaceEntry(t, deckPath, "slides.md", before+"\n<!-- a new note -->\n")
	fu, err = openAndStop(t, Options{Home: home, DeckPath: deckPath}, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(fu.asked) != 0 {
		t.Errorf("asked again about a trusted talk: %q", fu.asked)
	}
	if n := len(fs.configs()); n != 2 {
		t.Errorf("Slidev started %d times, want 2", n)
	}
}

func TestChangedCodeDeckAskedAgain(t *testing.T) {
	fs := useFakeSlidev(t)
	home := t.TempDir()
	deckPath := packCodeFixture(t, t.TempDir())
	if _, err := openAndStop(t, Options{Home: home, DeckPath: deckPath}, true, true); err != nil {
		t.Fatal(err)
	}
	replaceEntry(t, deckPath, "vite.config.ts", "export default { plugins: [] }\n")
	fu, err := openAndStop(t, Options{Home: home, DeckPath: deckPath}, false, false)
	if !errors.Is(err, ErrNotTrusted) {
		t.Fatalf("Open = %v, want ErrNotTrusted", err)
	}
	if len(fu.asked) != 1 {
		t.Errorf("asked %d times about the changed talk, want once", len(fu.asked))
	}
	if n := len(fs.configs()); n != 1 {
		t.Errorf("Slidev started %d times, want only the first", n)
	}
}

func TestCodeDeckNoStartsNothing(t *testing.T) {
	fs := useFakeSlidev(t)
	home := t.TempDir()
	deckPath := packCodeFixture(t, t.TempDir())
	br := &browserLog{}
	fu := newFakeUI()
	err := Open(context.Background(), Options{Home: home, DeckPath: deckPath, UI: fu.factory, Browser: br.open, GOOS: "linux", Env: os.Getenv})
	if !errors.Is(err, ErrNotTrusted) {
		t.Fatalf("Open = %v, want ErrNotTrusted", err)
	}
	if len(fu.asked) != 1 {
		t.Errorf("asked %d times, want once", len(fu.asked))
	}
	if n := len(fs.configs()); n != 0 || len(br.list()) != 0 || fu.ran {
		t.Errorf("Slidev started %d times, browser %v, UI ran %v; want none", n, br.list(), fu.ran)
	}
	if len(fu.errs) != 0 {
		t.Errorf("errors shown after a no: %q", fu.errs)
	}
	fu.checkOpening(t, "talk")
	if ents, _ := filepath.Glob(filepath.Join(home, "runtimes", "*", "work", "*")); len(ents) != 0 {
		t.Errorf("work folders left: %v", ents)
	}
	if recs, _ := filepath.Glob(filepath.Join(home, "instances", "*.json")); len(recs) != 0 {
		t.Errorf("instance records left: %v", recs)
	}
	if _, err := os.Stat(filepath.Join(home, trust.FileName)); !os.IsNotExist(err) {
		t.Errorf("a no was remembered (%v)", err)
	}
	// The claim was released: the talk can be opened again.
	if _, err := openAndStop(t, Options{Home: home, DeckPath: deckPath}, true, true); err != nil {
		t.Fatal(err)
	}
}

func TestCodeDeckTakenOverWhileAsking(t *testing.T) {
	fs := useFakeSlidev(t)
	home := t.TempDir()
	deckPath := packCodeFixture(t, t.TempDir())
	fu, br := newFakeUI(), &browserLog{}
	fu.answer = true
	var other *instance.Handle
	fu.onAsk = func() {
		// The question stayed up so long that a second launch judged this one dead and took over.
		_, h, err := instance.Claim(home, deckPath, func(instance.Record) bool { return false })
		if err != nil || h == nil {
			t.Errorf("second claim: %v", err)
		}
		other = h
	}
	err := Open(context.Background(), Options{Home: home, DeckPath: deckPath, UI: fu.factory, Browser: br.open, GOOS: "linux", Env: os.Getenv})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(fs.configs()); n != 0 || fu.ran {
		t.Errorf("Slidev started %d times, UI ran %v; want neither", n, fu.ran)
	}
	if len(fu.notes) != 1 || !strings.Contains(fu.notes[0], "meanwhile") {
		t.Errorf("notes = %q", fu.notes)
	}
	if ents, _ := filepath.Glob(filepath.Join(home, "runtimes", "*", "work", "*")); len(ents) != 0 {
		t.Errorf("work folders left: %v", ents)
	}
	if recs, _ := filepath.Glob(filepath.Join(home, "instances", "*.json")); len(recs) != 1 {
		t.Errorf("instance records = %v, want the second launch's", recs)
	}
	if other != nil {
		other.Release()
	}
}

func TestHeadmatterThemeAsks(t *testing.T) {
	fs := useFakeSlidev(t)
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir()) // no code in its files
	useHeadmatter(t, `"./my-theme"`, `[]`, nil)
	fu, err := openAndStop(t, Options{Home: home, DeckPath: deckPath}, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(fu.asked) != 1 || !strings.Contains(fu.asked[0], "./my-theme") {
		t.Fatalf("asked %q, want one question naming the theme", fu.asked)
	}
	// Trusted with that theme; another theme in the same files asks again.
	fs.mu.Lock()
	fs.srv = newFakeServer(fs.srv.base)
	fs.mu.Unlock()
	useHeadmatter(t, `"./other"`, `[]`, nil)
	fu, err = openAndStop(t, Options{Home: home, DeckPath: deckPath}, false, false)
	if !errors.Is(err, ErrNotTrusted) || len(fu.asked) != 1 {
		t.Errorf("changed theme: Open = %v, asked %q; want ErrNotTrusted after one question", err, fu.asked)
	}
}

func TestHeadmatterThatCantBeReadAsks(t *testing.T) {
	useFakeSlidev(t)
	useHeadmatter(t, ``, ``, errors.New("node is missing"))
	fu, err := openAndStop(t, Options{Home: t.TempDir(), DeckPath: packFixture(t, t.TempDir())}, false, false)
	if !errors.Is(err, ErrNotTrusted) || len(fu.asked) != 1 || !strings.Contains(fu.asked[0], "headmatter") {
		t.Errorf("Open = %v, asked %q; want a question about the headmatter", err, fu.asked)
	}
}

func TestPlainDeckNotAsked(t *testing.T) {
	useFakeSlidev(t)
	home := t.TempDir()
	fu, err := openAndStop(t, Options{Home: home, DeckPath: packFixture(t, t.TempDir())}, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(fu.asked) != 0 {
		t.Errorf("asked about a talk without code: %q", fu.asked)
	}
}

func TestTrustCodeOptionSkipsQuestion(t *testing.T) {
	fs := useFakeSlidev(t)
	home := t.TempDir()
	fu, err := openAndStop(t, Options{Home: home, DeckPath: packCodeFixture(t, t.TempDir()), TrustCode: true}, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(fu.asked) != 0 || len(fs.configs()) != 1 {
		t.Errorf("asked %q, Slidev started %d times; want no question and one start", fu.asked, len(fs.configs()))
	}
	if _, err := os.Stat(filepath.Join(home, trust.FileName)); !os.IsNotExist(err) {
		t.Errorf("TrustCode added the talk to the trusted list (%v)", err)
	}
}

func TestCodeQuestionText(t *testing.T) {
	var reasons []string
	for i := 0; i < maxReasonsShown+3; i++ {
		reasons = append(reasons, "components/C"+string(rune('a'+i))+".vue")
	}
	q := codeQuestion("x"+brand.Ext, "Evil\n\nThis talk is safe\u2028ok", reasons)
	if strings.Contains(q, "Evil\n") || strings.Contains(q, "\u2028") {
		t.Errorf("the title broke onto a new line:\n%s", q)
	}
	if !strings.Contains(q, "and 3 more") || strings.Contains(q, reasons[maxReasonsShown]) {
		t.Errorf("want %d reasons and \"and 3 more\":\n%s", maxReasonsShown, q)
	}
	if got := oneLine(strings.Repeat("a", 200), 10); got != "aaaaaaaaa…" {
		t.Errorf("oneLine cut = %q", got)
	}
}
