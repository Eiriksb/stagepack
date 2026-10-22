// Package workspace unpacks a deck file (brand.Ext) into a private work folder
// that Slidev runs in, and cleans such folders up.
package workspace

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

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
}

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
	for _, it := range items {
		if err := write(dir, it); err != nil {
			os.RemoveAll(dir)
			return Work{}, err
		}
	}
	return Work{Dir: dir, Manifest: m, Entries: names}, nil
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

// write streams one entry into dir.
func write(dir string, it item) error {
	dst := filepath.Join(dir, filepath.FromSlash(it.rel))
	if it.dir {
		return os.MkdirAll(dst, 0o755)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	rc, err := it.file.Open()
	if err != nil {
		return fmt.Errorf("open %s: %w", it.file.Name, err)
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return fmt.Errorf("extract %s: %w", it.file.Name, err)
	}
	return out.Close()
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
