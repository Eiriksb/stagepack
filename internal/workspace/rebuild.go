package workspace

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"stagepack/internal/brand"
	"stagepack/internal/deck"
)

// Rebuild writes a new deck at dst from the work folder, for when the deck
// file it came from is gone (a USB stick pulled). It holds the manifest with
// Updated set to now, then exactly w.Entries in their order, each read from
// where Extract put it (vendor/node_modules/* from node_modules/*); the slides
// entry is the work folder's current one, edits included. Nothing else in the
// work folder (Slidev's node_modules/.slidev, say) goes in. Entries are stored
// or deflated as pack does. dst is written to dst+".tmp" and renamed.
func Rebuild(w Work, dst string, now time.Time) (err error) {
	m := w.Manifest
	m.Updated = now
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(tmp)
		}
	}()
	zw := zip.NewWriter(f)
	if err := add(zw, brand.ManifestName, nil, append(manifest, '\n'), now); err != nil {
		return err
	}
	for _, name := range w.Entries {
		if strings.HasSuffix(name, "/") {
			if _, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store, Modified: now}); err != nil {
				return err
			}
			continue
		}
		src, err := os.Open(filepath.Join(w.Dir, filepath.FromSlash(deck.ExtractedPath(name))))
		if err != nil {
			return fmt.Errorf("rebuild: %w", err)
		}
		err = add(zw, name, src, nil, now)
		src.Close()
		if err != nil {
			return fmt.Errorf("rebuild: %s: %w", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// add writes one file entry from src, or from data when src is nil.
func add(zw *zip.Writer, name string, src io.Reader, data []byte, mod time.Time) error {
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: deck.EntryMethod(name), Modified: mod})
	if err != nil {
		return err
	}
	if src == nil {
		_, err = w.Write(data)
		return err
	}
	_, err = io.Copy(w, src)
	return err
}
