package save

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"stagepack/internal/brand"
	"stagepack/internal/deck"
)

// tmpWriter, when set, wraps the temp file's writer (tests inject failures).
var tmpWriter func(io.Writer) io.Writer

// maxManifest bounds how much of the manifest entry is read.
const maxManifest = 1 << 20

// tmpPattern names RewriteEntry's temp file: next to the deck, hidden, and
// unique per write (os.CreateTemp fills in the '*'), so two writers on one
// deck never write into or rename each other's temp file.
func tmpPattern(deckPath string) string {
	return "." + filepath.Base(deckPath) + ".*.tmp"
}

// RewriteEntry replaces the entry name of the deck at deckPath with data and
// sets the manifest's "updated" to now. Every other entry is copied raw (a
// stored entry stays stored, byte for byte) and in the same order; the new
// entry and manifest are deflated. The new deck is written to
// a unique <dir>/.<base>.<random>.tmp, synced and renamed over the original, so a crash leaves
// the original or the new deck, never a broken one. On any error the temp
// file is removed and the original is untouched. A missing entry is an error
// (it is not added); so is replacing the manifest itself.
func RewriteEntry(deckPath, name string, data []byte, now time.Time) (err error) {
	if name == brand.ManifestName {
		return fmt.Errorf("rewrite: %s is not a slides entry", name)
	}
	st, err := os.Stat(deckPath)
	if err != nil {
		return fmt.Errorf("rewrite: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(deckPath), tmpPattern(deckPath))
	if err != nil {
		return fmt.Errorf("rewrite: %w", err)
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(tmp)
		}
	}()
	// Keep the deck's permissions (CreateTemp makes 0600); Windows has only the read-only bit.
	if err = f.Chmod(st.Mode().Perm()); err != nil && runtime.GOOS != "windows" {
		return fmt.Errorf("rewrite: %w", err)
	}
	var w io.Writer = f
	if tmpWriter != nil {
		w = tmpWriter(f)
	}
	if err = writeReplaced(w, deckPath, name, data, now); err != nil {
		return fmt.Errorf("rewrite %s: %w", deckPath, err)
	}
	if err = f.Sync(); err != nil {
		return fmt.Errorf("rewrite: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("rewrite: %w", err)
	}
	if err = replaceFile(tmp, deckPath); err != nil {
		return fmt.Errorf("rewrite: %w", err)
	}
	syncDir(filepath.Dir(deckPath))
	return nil
}

// writeReplaced writes the deck at deckPath to w with entry name replaced.
// It closes the deck before returning, so the caller can rename over it (Windows).
func writeReplaced(w io.Writer, deckPath, name string, data []byte, now time.Time) error {
	zr, err := zip.OpenReader(deckPath)
	if err != nil {
		return err
	}
	defer zr.Close()

	var found, foundManifest int
	var manifest []byte
	for _, f := range zr.File {
		switch f.Name {
		case name:
			found++
		case brand.ManifestName:
			foundManifest++
			if manifest, err = readEntry(f); err != nil {
				return err
			}
		}
	}
	switch {
	case found == 0:
		return fmt.Errorf("no entry %s", name)
	case found > 1:
		return fmt.Errorf("more than one entry %s", name)
	case foundManifest != 1:
		return fmt.Errorf("not one %s", brand.ManifestName)
	}
	manifest, err = withUpdated(manifest, now)
	if err != nil {
		return err
	}

	zw := zip.NewWriter(w)
	if err := zw.SetComment(zr.Comment); err != nil {
		return err
	}
	for _, f := range zr.File {
		var err error
		switch f.Name {
		case name:
			err = deflated(zw, name, data, now)
		case brand.ManifestName:
			err = deflated(zw, brand.ManifestName, manifest, now)
		default:
			err = zw.Copy(f)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", f.Name, err)
		}
	}
	return zw.Close()
}

func deflated(zw *zip.Writer, name string, data []byte, mod time.Time) error {
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: mod})
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func readEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxManifest+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.Name, err)
	}
	if len(data) > maxManifest {
		return nil, fmt.Errorf("%s is too large", f.Name)
	}
	return data, nil
}

// withUpdated returns the manifest with "updated" set to now. The other
// fields keep their values; fields this build does not know are kept too.
func withUpdated(old []byte, now time.Time) ([]byte, error) {
	var m deck.Manifest
	if err := json.Unmarshal(old, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", brand.ManifestName, err)
	}
	m.Updated = now
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	var raw, known map[string]json.RawMessage
	if err := json.Unmarshal(old, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", brand.ManifestName, err)
	}
	if err := json.Unmarshal(out, &known); err != nil {
		return nil, err
	}
	extra := false
	for k, v := range raw {
		if _, ok := known[k]; !ok {
			known[k] = v
			extra = true
		}
	}
	if extra { // keys come out sorted; the values are what matters
		if out, err = json.MarshalIndent(known, "", "  "); err != nil {
			return nil, err
		}
	}
	return append(out, '\n'), nil
}
