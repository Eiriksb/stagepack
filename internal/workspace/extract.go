// Package workspace unpacks a deck file (brand.Ext) into a private work folder
// that Slidev runs in, and cleans such folders up.
package workspace

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"stagepack/internal/brand"
	"stagepack/internal/deck"
	slidevrt "stagepack/runtime"
)

// Work is one extracted deck.
type Work struct {
	Dir      string // <runtimeDir>/work/<random>
	Manifest deck.Manifest
	// Entries are the deck's entry names in zip order, the manifest left out.
	// Rebuild packs exactly these back.
	Entries []string
	// Code says why opening the deck runs code it brought along
	// (deck.CodeReasons); empty when it brings none.
	Code []string
	// Hash is a hex SHA-256 over the name and content of every entry but the
	// manifest and the slides file (which note edits change), and over Code.
	// The same talk with only its notes edited keeps its Hash.
	Hash string
}

// HasCode reports whether opening the deck runs code it brought along.
func (w Work) HasCode() bool { return len(w.Code) > 0 }

const defaultEntry = "slides.md"

const workName = "work"

// item is one validated entry: where it goes, relative to the work folder.
type item struct {
	file *zip.File
	rel  string // slash-separated, already mapped (vendor/node_modules -> node_modules)
	dir  bool
}

// Extract unpacks the deck into a new folder <runtimeDir>/work/<random>.
// vendor/node_modules/* lands in <Dir>/node_modules/*; the manifest is not
// written (it is returned). The deck is refused, with nothing created, if
// this viewer cannot open it or if any entry name is unsafe. The zip is
// closed before Extract returns.
func Extract(deckPath, runtimeDir string) (Work, error) {
	zr, err := zip.OpenReader(deckPath)
	if err != nil {
		return Work{}, fmt.Errorf("open %s: %w", deckPath, err)
	}
	defer zr.Close()

	m, err := deck.ReadManifest(&zr.Reader)
	if err != nil {
		return Work{}, err
	}
	if err := deck.Check(m, slidevrt.SlidevVersion()); err != nil {
		return Work{}, err
	}
	if m.Entry == "" {
		m.Entry = defaultEntry
	}
	if err := deck.CheckSlidesEntry(m.Entry); err != nil {
		return Work{}, fmt.Errorf("refusing deck: %w", err)
	}
	items, err := plan(zr.File)
	if err != nil {
		return Work{}, err
	}
	var names []string
	hasEntry := false
	for _, it := range items {
		names = append(names, it.file.Name)
		hasEntry = hasEntry || (!it.dir && it.file.Name == m.Entry)
	}
	if !hasEntry {
		return Work{}, fmt.Errorf("refusing deck: slides entry %q is not in it", m.Entry)
	}

	workRoot := filepath.Join(runtimeDir, workName)
	if err := os.MkdirAll(workRoot, 0o755); err != nil {
		return Work{}, err
	}
	dir, err := os.MkdirTemp(workRoot, "")
	if err != nil {
		return Work{}, err
	}
	var sums []string // "<name> <sha256>" of the entries Hash covers
	for _, it := range items {
		sum, err := write(dir, it)
		if err != nil {
			os.RemoveAll(dir)
			return Work{}, err
		}
		if it.file.Name != m.Entry {
			sums = append(sums, fmt.Sprintf("%q %x", it.file.Name, sum))
		}
	}
	read := func(name string) []byte {
		data, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(deck.ExtractedPath(name))))
		return data
	}
	w := Work{Dir: dir, Manifest: m, Entries: names, Code: deck.CodeReasons(names, m.Entry, read)}
	sort.Strings(sums)
	h := sha256.New()
	for _, l := range sums {
		fmt.Fprintln(h, l)
	}
	for _, r := range w.Code {
		fmt.Fprintf(h, "code %q\n", r)
	}
	w.Hash = hex.EncodeToString(h.Sum(nil))
	return w, nil
}

// plan validates every entry before anything is written, using the same
// rules as pack (deck.CheckEntryNames).
func plan(files []*zip.File) ([]item, error) {
	names := make([]string, len(files))
	for i, f := range files {
		if f.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("refusing entry %q: links are not allowed", f.Name)
		}
		names[i] = f.Name
	}
	paths, dirs, err := deck.CheckEntryNames(names)
	if err != nil {
		return nil, fmt.Errorf("refusing %w", err)
	}
	var items []item
	for i, f := range files {
		if f.Name == brand.ManifestName {
			continue
		}
		if !dirs[i] && f.Mode().IsDir() {
			return nil, fmt.Errorf("refusing entry %q: folder without trailing slash", f.Name)
		}
		items = append(items, item{file: f, rel: paths[i], dir: dirs[i]})
	}
	return items, nil
}

// write streams one entry into dir and returns the SHA-256 of what it wrote
// (nil for a folder).
func write(dir string, it item) ([]byte, error) {
	dst := filepath.Join(dir, filepath.FromSlash(it.rel))
	if it.dir {
		return nil, os.MkdirAll(dst, 0o755)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, err
	}
	rc, err := it.file.Open()
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", it.file.Name, err)
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), rc); err != nil {
		out.Close()
		return nil, fmt.Errorf("extract %s: %w", it.file.Name, err)
	}
	return h.Sum(nil), out.Close()
}

// Remove deletes the work folder with everything in it, Slidev's own
// node_modules/.slidev included.
func Remove(w Work) error {
	if w.Dir == "" {
		return errors.New("no work folder to remove")
	}
	return os.RemoveAll(w.Dir)
}

// CleanStale removes every folder under <runtimeDir>/work that alive says no
// running instance owns. alive gets the folder's full path.
func CleanStale(runtimeDir string, alive func(dir string) bool) error {
	root := filepath.Join(runtimeDir, workName)
	ents, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if alive(dir) {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
