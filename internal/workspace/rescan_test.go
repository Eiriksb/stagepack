package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

// Rescan gives the Code and Hash that Extract gives the same files, so a saved talk keeps the
// version the next open computes, and trust carried forward matches it.
func TestRescanMatchesExtract(t *testing.T) {
	p := writeDeck(t, "53.0.0", entry{name: "slides.md", data: "# hi"}, entry{name: "public/", mode: os.ModeDir | 0o755},
		entry{name: "public/a.png", data: "png"}, entry{name: "vite.config.ts", data: "export default {}"})
	w, err := Extract(p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Remove(w) })
	extracted := w
	w.AddCode(`the theme "./t"`)
	w.Rescan()
	if w.Hash != extracted.Hash || !sameList(w.Code, extracted.Code) {
		t.Errorf("Rescan of the extracted folder: Code %q, Hash %s; want %q, %s (what AddCode added is dropped)",
			w.Code, w.Hash, extracted.Code, extracted.Hash)
	}

	// After a save that adds code and a file, Rescan agrees with a fresh Extract of the saved deck.
	put(t, filepath.Join(w.Dir, "setup", "main.ts"), "export default () => {}")
	put(t, filepath.Join(w.Dir, "public", "b.png"), "png2")
	put(t, filepath.Join(w.Dir, "slides.md"), "# edited")
	if err := SaveFolder(&w, p, saveNow); err != nil {
		t.Fatal(err)
	}
	w.Rescan()
	fresh, err := Extract(p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Remove(fresh) })
	if w.Hash != fresh.Hash || !sameList(w.Code, fresh.Code) {
		t.Errorf("after a save, Rescan: Code %q, Hash %s; Extract: Code %q, Hash %s", w.Code, w.Hash, fresh.Code, fresh.Hash)
	}
	if w.Hash == extracted.Hash {
		t.Error("the saved talk kept the old hash")
	}
}
