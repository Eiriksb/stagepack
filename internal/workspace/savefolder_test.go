package workspace

import (
	"archive/zip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"stagepack/internal/brand"
	"stagepack/internal/pack"
	"stagepack/internal/save"
)

var saveNow = time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

// openForSave packs the fixture talk into a deck file and extracts it.
func openForSave(t *testing.T) (Work, string) {
	t.Helper()
	deckPath := filepath.Join(t.TempDir(), "a b%c", "my talk æøå 100%"+brand.Ext)
	if err := os.MkdirAll(filepath.Dir(deckPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := pack.Pack(pack.Options{Dir: fixture, Out: deckPath, Warn: io.Discard}); err != nil {
		t.Fatal(err)
	}
	w, err := Extract(deckPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Remove(w) })
	return w, deckPath
}

type zipEntry struct {
	data     string
	raw      string // the compressed bytes as stored in the deck
	method   uint16
	crc      uint32
	modified time.Time
}

func readDeck(t *testing.T, deckPath string) (map[string]zipEntry, []string) {
	t.Helper()
	zr, err := zip.OpenReader(deckPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	got := map[string]zipEntry{}
	var names []string
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		rr, err := f.OpenRaw()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(rr)
		if err != nil {
			t.Fatal(err)
		}
		got[f.Name] = zipEntry{string(b), string(raw), f.Method, f.CRC32, f.Modified}
		names = append(names, f.Name)
	}
	return got, names
}

func put(t *testing.T, p, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSaveFolderUnchanged(t *testing.T) {
	w, deckPath := openForSave(t)
	before, namesBefore := readDeck(t, deckPath)
	if err := SaveFolder(&w, deckPath, saveNow); err != nil {
		t.Fatal(err)
	}
	after, namesAfter := readDeck(t, deckPath)
	if !reflect.DeepEqual(namesBefore, namesAfter) {
		t.Errorf("names = %q, want %q", namesAfter, namesBefore)
	}
	for name, e := range before {
		if name != brand.ManifestName && after[name] != e {
			t.Errorf("%s changed", name)
		}
	}
	if !w.Manifest.Updated.Equal(saveNow) {
		t.Errorf("w.Manifest.Updated = %v", w.Manifest.Updated)
	}
	if !strings.Contains(after[brand.ManifestName].data, `"updated": "2030-01-02T03:04:05Z"`) {
		t.Errorf("manifest = %s", after[brand.ManifestName].data)
	}
	if len(w.Sums) == 0 || w.Sums["slides.md"] == "" {
		t.Errorf("Sums = %v", w.Sums)
	}
}

func TestSaveFolderChangedAndNew(t *testing.T) {
	w, deckPath := openForSave(t)
	before, _ := readDeck(t, deckPath)
	put(t, filepath.Join(w.Dir, "slides.md"), "# edited\n")
	put(t, filepath.Join(w.Dir, "public", "new image æ.png"), "PNGDATA")
	if err := SaveFolder(&w, deckPath, saveNow); err != nil {
		t.Fatal(err)
	}
	after, _ := readDeck(t, deckPath)
	if after["slides.md"].data != "# edited\n" {
		t.Errorf("slides.md = %q", after["slides.md"].data)
	}
	if e := after["public/new image æ.png"]; e.data != "PNGDATA" || e.method != zip.Store {
		t.Errorf("new image = %+v", e)
	}
	for name, e := range before {
		if name != brand.ManifestName && name != "slides.md" && after[name] != e {
			t.Errorf("%s changed", name)
		}
	}
	found := false
	for _, n := range w.Entries {
		found = found || n == "public/new image æ.png"
	}
	if !found || w.Sums["public/new image æ.png"] == "" {
		t.Errorf("Entries = %q, Sums = %v", w.Entries, w.Sums)
	}
	// The next save copies every entry but the manifest raw, new ones included.
	if err := SaveFolder(&w, deckPath, saveNow.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	again, _ := readDeck(t, deckPath)
	for name, e := range after {
		if name != brand.ManifestName && again[name] != e {
			t.Errorf("%s was re-encoded by the second save", name)
		}
	}
}

func TestSaveFolderDeleted(t *testing.T) {
	w, deckPath := openForSave(t)
	if err := os.Remove(filepath.Join(w.Dir, "public", "clip.mp4")); err != nil {
		t.Fatal(err)
	}
	if err := SaveFolder(&w, deckPath, saveNow); err != nil {
		t.Fatal(err)
	}
	after, _ := readDeck(t, deckPath)
	if _, ok := after["public/clip.mp4"]; ok {
		t.Error("public/clip.mp4 still in the deck")
	}
	if _, ok := w.Sums["public/clip.mp4"]; ok {
		t.Error("Sums still has public/clip.mp4")
	}
	for _, n := range w.Entries {
		if n == "public/clip.mp4" {
			t.Error("Entries still has public/clip.mp4")
		}
	}
}

func TestSaveFolderSkips(t *testing.T) {
	w, deckPath := openForSave(t)
	put(t, filepath.Join(w.Dir, ".slidev", "snapshots.json"), "{}")
	put(t, filepath.Join(w.Dir, "dist", "x"), "x")
	put(t, filepath.Join(w.Dir, "node_modules", ".slidev", "y"), "y")
	put(t, filepath.Join(w.Dir, ".git", "HEAD"), "ref")
	if err := SaveFolder(&w, deckPath, saveNow); err != nil {
		t.Fatal(err)
	}
	after, _ := readDeck(t, deckPath)
	for name := range after {
		for _, bad := range []string{".slidev", "dist", ".git", "snapshots", "/y"} {
			if strings.Contains(name, bad) {
				t.Errorf("entry %s should have been skipped", name)
			}
		}
	}
}

func TestSaveFolderVendoredMapsBack(t *testing.T) {
	w, deckPath := openForSave(t)
	put(t, filepath.Join(w.Dir, "node_modules", "fixture-dep", "index.css"), "a{b:c}")
	put(t, filepath.Join(w.Dir, "node_modules", "other", "package.json"), "{}")
	if err := SaveFolder(&w, deckPath, saveNow); err != nil {
		t.Fatal(err)
	}
	after, _ := readDeck(t, deckPath)
	if after["vendor/node_modules/fixture-dep/index.css"].data != "a{b:c}" {
		t.Errorf("vendored file = %+v", after["vendor/node_modules/fixture-dep/index.css"])
	}
	if _, ok := after["vendor/node_modules/fixture-dep/package.json"]; !ok {
		t.Error("vendored package.json lost")
	}
	for name := range after {
		if strings.Contains(name, "other") || strings.HasPrefix(name, "node_modules") {
			t.Errorf("unexpected entry %s", name)
		}
	}
}

func TestSaveFolderConflict(t *testing.T) {
	w, deckPath := openForSave(t)
	if err := save.RewriteEntry(deckPath, "slides.md", []byte("# elsewhere\n"), saveNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	orig, err := os.ReadFile(deckPath)
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(w.Dir, "slides.md"), "# mine\n")
	err = SaveFolder(&w, deckPath, saveNow)
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *ConflictError", err)
	}
	if !ce.Want.Equal(w.Manifest.Updated) || ce.Got.IsZero() {
		t.Errorf("ConflictError = %+v", ce)
	}
	now, _ := os.ReadFile(deckPath)
	if string(now) != string(orig) {
		t.Error("the deck changed")
	}
}

func TestSaveFolderBadName(t *testing.T) {
	w, deckPath := openForSave(t)
	orig, _ := os.ReadFile(deckPath)
	put(t, filepath.Join(w.Dir, "public", "a:b.png"), "x")
	err := SaveFolder(&w, deckPath, saveNow)
	var be *BadNamesError
	if !errors.As(err, &be) || !reflect.DeepEqual(be.Names, []string{"public/a:b.png"}) {
		t.Fatalf("err = %v, want *BadNamesError{public/a:b.png}", err)
	}
	if !strings.Contains(err.Error(), "public/a:b.png") {
		t.Errorf("message = %q", err)
	}
	now, _ := os.ReadFile(deckPath)
	if string(now) != string(orig) {
		t.Error("the deck changed")
	}
	ents, _ := os.ReadDir(filepath.Dir(deckPath))
	if len(ents) != 1 {
		t.Errorf("deck folder has %d entries, want 1", len(ents))
	}
}

func TestSaveFolderRefusesMissingSlides(t *testing.T) {
	w, deckPath := openForSave(t)
	orig, _ := os.ReadFile(deckPath)
	if err := os.Remove(filepath.Join(w.Dir, "slides.md")); err != nil {
		t.Fatal(err)
	}
	err := SaveFolder(&w, deckPath, saveNow)
	if err == nil || !strings.Contains(err.Error(), "slides.md") {
		t.Fatalf("err = %v, want one naming slides.md", err)
	}
	now, _ := os.ReadFile(deckPath)
	if string(now) != string(orig) {
		t.Error("the deck changed")
	}
	if ents, _ := os.ReadDir(filepath.Dir(deckPath)); len(ents) != 1 {
		t.Errorf("deck folder has %d entries, want 1", len(ents))
	}
}

// Slidev writes slides.md in place, so a save can catch it empty, mid-write.
func TestSaveFolderRefusesEmptySlides(t *testing.T) {
	w, deckPath := openForSave(t)
	orig, _ := os.ReadFile(deckPath)
	if err := os.WriteFile(filepath.Join(w.Dir, "slides.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	err := SaveFolder(&w, deckPath, saveNow)
	if err == nil || err.Error() != "the slides file is being written; save again in a moment" {
		t.Fatalf("err = %v, want the mid-write refusal", err)
	}
	now, _ := os.ReadFile(deckPath)
	if string(now) != string(orig) {
		t.Error("the deck changed")
	}
	if ents, _ := os.ReadDir(filepath.Dir(deckPath)); len(ents) != 1 {
		t.Errorf("deck folder has %d entries, want 1: no temp file", len(ents))
	}
}

func TestSaveFolderRefusesSymlinks(t *testing.T) {
	w, deckPath := openForSave(t)
	orig, _ := os.ReadFile(deckPath)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	put(t, outside, "SECRET")
	if err := os.Symlink(outside, filepath.Join(w.Dir, "public", "link.txt")); err != nil {
		t.Skip("no symlinks:", err)
	}
	err := SaveFolder(&w, deckPath, saveNow)
	if err == nil || !strings.Contains(err.Error(), "public/link.txt") {
		t.Fatalf("err = %v, want one naming public/link.txt", err)
	}
	now, _ := os.ReadFile(deckPath)
	if string(now) != string(orig) || strings.Contains(string(now), "SECRET") {
		t.Error("the deck changed")
	}
}
