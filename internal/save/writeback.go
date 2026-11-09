package save

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"stagepack/internal/atomicfile"
	"stagepack/internal/brand"
	"stagepack/internal/deck"
)

// tmpWriter, when set, wraps the temp file's writer (tests inject failures).
var tmpWriter func(io.Writer) io.Writer

// maxManifest bounds how much of the manifest entry is read.
const maxManifest = 1 << 20

// RewriteEntry replaces the entry name of the deck at deckPath with data and
// sets the manifest's "updated" to now. Every other entry is copied raw (a
// stored entry stays stored, byte for byte) and in the same order; the new
// entry and manifest are deflated. The new deck is written to
// a unique <dir>/.<base>.<random>.tmp (atomicfile.Replace), synced and renamed over the original, so a crash leaves
// the original or the new deck, never a broken one. On any error the temp
// file is removed and the original is untouched. A missing entry is an error
// (it is not added); so is replacing the manifest itself.
func RewriteEntry(deckPath, name string, data []byte, now time.Time) error {
	if name == brand.ManifestName {
		return fmt.Errorf("rewrite: %s is not a slides entry", name)
	}
	var werr error
	err := atomicfile.Replace(deckPath, func(w io.Writer) error {
		if tmpWriter != nil {
			w = tmpWriter(w)
		}
		if werr = writeReplaced(w, deckPath, name, data, now); werr != nil {
			werr = fmt.Errorf("rewrite %s: %w", deckPath, werr)
		}
		return werr
	})
	if err != nil && err != werr {
		return fmt.Errorf("rewrite: %w", err)
	}
	return err
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
	manifest, err = WithUpdated(manifest, now)
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

// WithUpdated returns the manifest with "updated" set to now. The other
// fields keep their values; fields this build does not know are kept too.
func WithUpdated(old []byte, now time.Time) ([]byte, error) {
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
