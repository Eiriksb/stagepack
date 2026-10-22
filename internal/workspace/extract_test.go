package workspace

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"stagepack/internal/brand"
	"stagepack/internal/deck"
	"stagepack/internal/pack"
)

const fixture = "../../testdata/deck"

type entry struct {
	name string
	data string
	mode fs.FileMode // 0 = regular file
}

func manifestJSON(t *testing.T, slidev string) []byte {
	t.Helper()
	b, err := json.Marshal(deck.Manifest{Format: deck.FormatVersion, ID: "id1", Title: "T", Slidev: slidev, Entry: "slides.md"})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// writeDeck writes a hand-made deck file with the given entries (raw names,
// in order) and returns its path. A manifest for slidev is added first unless
// slidev is empty.
func writeDeck(t *testing.T, slidev string, entries ...entry) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(e entry) {
		h := &zip.FileHeader{Name: e.name, Method: zip.Store}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.data)); err != nil {
			t.Fatal(err)
		}
	}
	if slidev != "" {
		add(entry{name: brand.ManifestName, data: string(manifestJSON(t, slidev))})
	}
	for _, e := range entries {
		add(e)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "talk"+brand.Ext)
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// listTree returns every path under root (relative, sorted), with dirs marked.
func listTree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			rel += "/"
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func sameList(a, b []string) bool { return strings.Join(a, "\n") == strings.Join(b, "\n") }

func TestExtractLayout(t *testing.T) {
	deckPath := filepath.Join(t.TempDir(), "talk"+brand.Ext)
	if _, err := pack.Pack(pack.Options{Dir: fixture, Out: deckPath, Warn: io.Discard}); err != nil {
		t.Fatal(err)
	}
	rt := t.TempDir()
	w, err := Extract(deckPath, rt)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(rt, "work"); filepath.Dir(w.Dir) != want {
		t.Errorf("Dir %q is not directly under %q", w.Dir, want)
	}
	if w.Manifest.Entry != "slides.md" || w.Manifest.Format != deck.FormatVersion {
		t.Errorf("manifest = %+v", w.Manifest)
	}
	for _, name := range []string{"slides.md", "public/clip.mp4", "node_modules/fixture-dep/package.json"} {
		if _, err := os.Stat(filepath.Join(w.Dir, filepath.FromSlash(name))); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	want, err := os.ReadFile(filepath.Join(fixture, "public", "clip.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(w.Dir, "public", "clip.mp4"))
	if err != nil || !bytes.Equal(got, want) {
		t.Errorf("clip.mp4 differs (err %v)", err)
	}
	if _, err := os.Stat(filepath.Join(w.Dir, "vendor")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("vendor/ exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.Dir, brand.ManifestName)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s was extracted: %v", brand.ManifestName, err)
	}
	// a second extraction of the same deck gets its own folder
	w2, err := Extract(deckPath, rt)
	if err != nil {
		t.Fatal(err)
	}
	if w2.Dir == w.Dir {
		t.Error("two extractions share a folder")
	}
}

func TestExtractLargeFileIntact(t *testing.T) {
	big := strings.Repeat("0123456789abcdef", 1<<20) // 16 MiB
	p := writeDeck(t, "53.0.0", entry{name: "slides.md", data: "# hi"}, entry{name: "public/big.mp4", data: big})
	w, err := Extract(p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(w.Dir, "public", "big.mp4"))
	if err != nil || string(got) != big {
		t.Errorf("big.mp4 differs (err %v)", err)
	}
}

func TestExtractRefusesBadPaths(t *testing.T) {
	bad := []string{
		"../evil", "/abs", `C:\x`, "C:/x", "a/../../evil", "a/b/../../../evil",
		"vendor/node_modules/../../../evil", `a\b`, "..", "a/./b/../../..", "x:y", "a//b",
		"CON", "dir/nul.txt", "aux.md", "trail.", "trail ", "a/\x00b", "",
	}
	for _, name := range bad {
		t.Run(name, func(t *testing.T) {
			p := writeDeck(t, "53.0.0",
				entry{name: "slides.md", data: "# ok"},
				entry{name: "public/clip.mp4", data: "ok"},
				entry{name: name, data: "evil"})
			parent := t.TempDir()
			rt := filepath.Join(parent, "rt")
			if err := os.Mkdir(rt, 0o755); err != nil {
				t.Fatal(err)
			}
			before := listTree(t, parent)
			_, err := Extract(p, rt)
			if err == nil {
				t.Fatal("want an error")
			}
			if after := listTree(t, parent); !sameList(before, after) {
				t.Errorf("files appeared:\nbefore %v\nafter  %v", before, after)
			}
		})
	}
}

func TestExtractRefusesSymlinkEntry(t *testing.T) {
	p := writeDeck(t, "53.0.0", entry{name: "slides.md", data: "# ok"},
		entry{name: "link", data: "/etc/passwd", mode: os.ModeSymlink | 0o777})
	rt := t.TempDir()
	before := listTree(t, rt)
	if _, err := Extract(p, rt); err == nil {
		t.Fatal("want an error for a symlink entry")
	}
	if after := listTree(t, rt); !sameList(before, after) {
		t.Errorf("files appeared: %v", after)
	}
}

func TestExtractRefusesConflicts(t *testing.T) {
	cases := map[string][]entry{
		"duplicate":           {{name: "slides.md", data: "a"}, {name: "slides.md", data: "b"}},
		"duplicate by case":   {{name: "slides.md", data: "a"}, {name: "Slides.md", data: "b"}},
		"duplicate by vendor": {{name: "node_modules/x/a.js", data: "a"}, {name: "vendor/node_modules/x/a.js", data: "b"}},
		"file then dir":       {{name: "a", data: "file"}, {name: "a/b.md", data: "x"}},
		"dir then file":       {{name: "a/b.md", data: "x"}, {name: "a", data: "file"}},
		"dir entry vs file":   {{name: "a", data: "file"}, {name: "a/", mode: os.ModeDir | 0o755}},
	}
	for name, es := range cases {
		t.Run(name, func(t *testing.T) {
			p := writeDeck(t, "53.0.0", es...)
			rt := t.TempDir()
			before := listTree(t, rt)
			_, err := Extract(p, rt)
			if err == nil {
				t.Fatal("want an error")
			}
			if after := listTree(t, rt); !sameList(before, after) {
				t.Errorf("files appeared: %v", after)
			}
		})
	}
}

func TestExtractAcceptsDirectoryEntries(t *testing.T) {
	p := writeDeck(t, "53.0.0",
		entry{name: "slides.md", data: "# hi"},
		entry{name: "public/", mode: os.ModeDir | 0o755},
		entry{name: "public/a.txt", data: "a"},
		entry{name: "empty/", mode: os.ModeDir | 0o755})
	w, err := Extract(p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(w.Dir, "empty")); err != nil || !st.IsDir() {
		t.Errorf("empty dir missing: %v", err)
	}
}

func TestExtractRefusesTooNew(t *testing.T) {
	p := writeDeck(t, "54.0.0", entry{name: "slides.md", data: "# hi"})
	rt := t.TempDir()
	_, err := Extract(p, rt)
	var tn *deck.TooNewError
	if !errors.As(err, &tn) {
		t.Fatalf("err = %v (%T), want *deck.TooNewError", err, err)
	}
	if _, err := os.Stat(filepath.Join(rt, "work")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("work folder was created: %v", err)
	}
}

func TestExtractRefusesOlder(t *testing.T) {
	p := writeDeck(t, "52.0.0", entry{name: "slides.md", data: "# hi"})
	_, err := Extract(p, t.TempDir())
	var tn *deck.TooNewError
	if !errors.As(err, &tn) || !tn.Older {
		t.Fatalf("err = %v, want *deck.TooNewError with Older", err)
	}
}

func TestExtractNotADeck(t *testing.T) {
	p := writeDeck(t, "", entry{name: "slides.md", data: "# hi"})
	if _, err := Extract(p, t.TempDir()); err == nil || !strings.Contains(err.Error(), brand.ManifestName) {
		t.Errorf("err = %v, want one mentioning %s", err, brand.ManifestName)
	}
	garbage := filepath.Join(t.TempDir(), "x"+brand.Ext)
	if err := os.WriteFile(garbage, []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Extract(garbage, t.TempDir()); err == nil {
		t.Error("want an error for a non-zip file")
	}
	if _, err := Extract(filepath.Join(t.TempDir(), "missing"+brand.Ext), t.TempDir()); err == nil {
		t.Error("want an error for a missing file")
	}
}

func TestExtractLeavesDeckUnlocked(t *testing.T) {
	// the deck file can be removed right after Extract returns (matters on Windows)
	p := writeDeck(t, "53.0.0", entry{name: "slides.md", data: "# hi"})
	if _, err := Extract(p, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
}

func TestRemove(t *testing.T) {
	p := writeDeck(t, "53.0.0", entry{name: "slides.md", data: "# hi"})
	w, err := Extract(p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Slidev writes node_modules/.slidev/ itself
	if err := os.MkdirAll(filepath.Join(w.Dir, "node_modules", ".slidev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Remove(w); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Dir still exists: %v", err)
	}
	if err := Remove(Work{}); err == nil {
		t.Error("Remove of a zero Work must refuse")
	}
}

func TestCleanStale(t *testing.T) {
	rt := t.TempDir()
	a := filepath.Join(rt, "work", "a")
	b := filepath.Join(rt, "work", "b")
	for _, d := range []string{a, filepath.Join(b, "node_modules", ".slidev")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := CleanStale(rt, func(dir string) bool { return dir == a }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a); err != nil {
		t.Errorf("alive folder removed: %v", err)
	}
	if _, err := os.Stat(b); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stale folder kept: %v", err)
	}
}

func TestCleanStaleNoWorkFolder(t *testing.T) {
	if err := CleanStale(t.TempDir(), func(string) bool { return false }); err != nil {
		t.Fatal(err)
	}
}

// writeDeckEntry is writeDeck with a manifest naming entryName as the slides file.
func writeDeckEntry(t *testing.T, entryName string, entries ...entry) string {
	t.Helper()
	p := writeDeck(t, "", entries...)
	m, err := json.Marshal(deck.Manifest{Format: deck.FormatVersion, ID: "id1", Title: "T", Slidev: "53.0.0", Entry: entryName})
	if err != nil {
		t.Fatal(err)
	}
	return writeDeck(t, "", append([]entry{{name: brand.ManifestName, data: string(m)}}, readEntries(t, p)...)...)
}

func readEntries(t *testing.T, p string) []entry {
	t.Helper()
	zr, err := zip.OpenReader(p)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var out []entry
	for _, f := range zr.File {
		rc, _ := f.Open()
		data, _ := io.ReadAll(rc)
		rc.Close()
		e := entry{name: f.Name, data: string(data)}
		if f.Mode().IsDir() {
			e.mode = os.ModeDir | 0o755
		}
		out = append(out, e)
	}
	return out
}

func TestExtractRefusesBadEntry(t *testing.T) {
	files := []entry{{name: "slides.md", data: "# a"}, {name: "-x.md", data: "# b"}, {name: "sub/x.md", data: "# c"}, {name: "x.txt", data: "d"}}
	for _, e := range []string{"../x.md", "-x.md", "sub/x.md", "missing.md", "x.txt", "sub/"} {
		p := writeDeckEntry(t, e, files...)
		rt := t.TempDir()
		_, err := Extract(p, rt)
		if err == nil || !strings.Contains(err.Error(), "entry") {
			t.Errorf("entry %q: err = %v, want a refusal naming the entry", e, err)
		}
		if _, err := os.Stat(filepath.Join(rt, "work")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("entry %q: work folder created before refusing (%v)", e, err)
		}
	}
	// A plain name is fine, and an empty one means slides.md.
	for _, e := range []string{"slides.md", ""} {
		w, err := Extract(writeDeckEntry(t, e, files...), t.TempDir())
		if err != nil || w.Manifest.Entry != "slides.md" {
			t.Errorf("entry %q: %v, manifest entry %q", e, err, w.Manifest.Entry)
		}
	}
}

func TestExtractListsEntries(t *testing.T) {
	p := writeDeck(t, "53.0.0", entry{name: "public/", mode: os.ModeDir | 0o755}, entry{name: "public/a.mp4", data: "v"},
		entry{name: "slides.md", data: "# hi"}, entry{name: "vendor/node_modules/x/index.js", data: "x"})
	w, err := Extract(p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"public/", "public/a.mp4", "slides.md", "vendor/node_modules/x/index.js"}
	if !sameList(w.Entries, want) {
		t.Errorf("Entries = %q, want %q", w.Entries, want)
	}
}

func TestRebuild(t *testing.T) {
	video := strings.Repeat("\x00\x01video", 1000)
	p := writeDeck(t, "53.0.0", entry{name: "public/", mode: os.ModeDir | 0o755}, entry{name: "public/a.mp4", data: video},
		entry{name: "slides.md", data: "# hi"}, entry{name: "vendor/node_modules/x/index.js", data: "module.exports = 1"},
		entry{name: "empty/", mode: os.ModeDir | 0o755})
	w, err := Extract(p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// What Slidev and the presenter leave in the work folder.
	os.WriteFile(filepath.Join(w.Dir, "slides.md"), []byte("# hi\n<!-- edited -->\n"), 0o644)
	os.MkdirAll(filepath.Join(w.Dir, "node_modules", ".slidev"), 0o755)
	os.WriteFile(filepath.Join(w.Dir, "node_modules", ".slidev", "junk.js"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(w.Dir, "stray.txt"), []byte("x"), 0o644)

	os.Remove(p) // the source deck is gone
	dst := filepath.Join(t.TempDir(), "copy"+brand.Ext)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	if err := Rebuild(w, dst, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dst + ".tmp"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("temp file left: %v", err)
	}
	zr, err := zip.OpenReader(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		wantMethod := zip.Store
		if strings.HasSuffix(f.Name, ".md") || strings.HasSuffix(f.Name, ".js") || f.Name == brand.ManifestName {
			wantMethod = zip.Deflate
		}
		if !strings.HasSuffix(f.Name, "/") && f.Method != wantMethod {
			t.Errorf("%s: method %d, want %d", f.Name, f.Method, wantMethod)
		}
	}
	want := []string{brand.ManifestName, "public/", "public/a.mp4", "slides.md", "vendor/node_modules/x/index.js", "empty/"}
	if !sameList(names, want) {
		t.Errorf("entries = %q, want %q", names, want)
	}
	m, err := deck.ReadManifest(&zr.Reader)
	if err != nil || !m.Updated.Equal(now) || m.ID != "id1" {
		t.Errorf("manifest = %+v, %v", m, err)
	}
	w2, err := Extract(dst, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(w2.Dir, "slides.md")); !strings.Contains(string(got), "edited") {
		t.Errorf("slides.md = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(w2.Dir, "public", "a.mp4")); string(got) != video {
		t.Error("a.mp4 differs")
	}
	if got, _ := os.ReadFile(filepath.Join(w2.Dir, "node_modules", "x", "index.js")); string(got) != "module.exports = 1" {
		t.Errorf("index.js = %q", got)
	}
}

func TestExtractClassifiesCode(t *testing.T) {
	deckPath := filepath.Join(t.TempDir(), "talk"+brand.Ext)
	if _, err := pack.Pack(pack.Options{Dir: fixture, Out: deckPath, Warn: io.Discard}); err != nil {
		t.Fatal(err)
	}
	w, err := Extract(deckPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if w.HasCode() || w.Hash == "" {
		t.Errorf("fixture: Code = %q, Hash = %q; want no code and a hash", w.Code, w.Hash)
	}

	p := writeDeck(t, "53.0.0", entry{name: "slides.md", data: "---\ntheme: ./theme\n---\n# hi"},
		entry{name: "vite.config.ts", data: "export default {}"}, entry{name: "public/a.js", data: "x"},
		entry{name: "vendor/node_modules/x/index.js", data: "x"},
		entry{name: "vendor/node_modules/y/package.json", data: `{"slidev": {"addons": ["/elsewhere"]}}`},
		entry{name: "package.json", data: `{"slidev": {"addons": ["./addon"]}}`})
	w, err = Extract(p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"vite.config.ts (configuration that runs as code)", "public/a.js", "package x (bundled with the talk)",
		"package y (bundled with the talk)", "package.json (names addon ./addon, loaded from a folder)", "theme ./theme in slides.md (loaded from a folder)"}
	if !w.HasCode() || !sameList(w.Code, want) {
		t.Errorf("Code = %q, want %q", w.Code, want)
	}
}

func TestExtractHash(t *testing.T) {
	hash := func(entries ...entry) string {
		t.Helper()
		w, err := Extract(writeDeck(t, "53.0.0", entries...), t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return w.Hash
	}
	slides := entry{name: "slides.md", data: "# hi"}
	cfg := entry{name: "vite.config.ts", data: "export default {}"}
	base := hash(slides, cfg)
	if got := hash(entry{name: "slides.md", data: "# hi\n<!-- a note -->"}, cfg); got != base {
		t.Error("editing the slides file changed the hash")
	}
	if got := hash(cfg, slides); got != base {
		t.Error("the order of the entries changed the hash")
	}
	for name, entries := range map[string][]entry{
		"changed config":                      {slides, {name: "vite.config.ts", data: "export default { x: 1 }"}},
		"renamed config":                      {slides, {name: "vite.config.mts", data: cfg.data}},
		"added file":                          {slides, cfg, {name: "public/a.png", data: "png"}},
		"theme by path":                       {{name: "slides.md", data: "---\ntheme: ./t\n---\n# hi"}, cfg},
		"theme in a code block of the slides": {{name: "slides.md", data: "# hi\n```yaml\ntheme: ./t\n```\n"}, cfg},
	} {
		if hash(entries...) == base {
			t.Errorf("%s: same hash", name)
		}
	}
}
