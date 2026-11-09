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
	"strings"
	"time"

	"stagepack/internal/atomicfile"
	"stagepack/internal/brand"
	"stagepack/internal/deck"
	"stagepack/internal/save"
)

// ConflictError means the deck file was changed by another program since the
// work folder was opened: its manifest's Updated is no longer Want.
type ConflictError struct{ Want, Got time.Time }

func (e *ConflictError) Error() string {
	return "the file was changed by another program since it was opened"
}

// BadNamesError means the work folder holds files whose names a deck file
// cannot hold (see deck.CleanEntryName). Names are the entry names.
type BadNamesError struct{ Names []string }

func (e *BadNamesError) Error() string {
	return "these files have names a " + brand.Ext + " can't hold: " + strings.Join(e.Names, ", ")
}

const (
	vendorEntryPrefix = "vendor/node_modules/"
	maxManifestSize   = 1 << 20
)

// SaveFolder writes the whole work folder back into the deck file at
// deckPath, atomically (atomicfile.Replace): the deck is the old one or the
// new one, never a mix.
//
// What goes in is the project's files (deck.ProjectFiles: no node_modules,
// .git, top-level dist or .slidev), plus the entries that were vendored
// (w.Entries under vendor/node_modules/), read back from node_modules/. A
// package that is not already vendored is never added. A file whose sum is
// that in w.Sums is copied raw from the old deck; a changed or new file is
// stored or deflated as pack does (deck.EntryMethod); a deleted file drops
// out. The old order is kept, new files follow sorted by name. The manifest
// is rewritten with Updated set to now (WithUpdated).
//
// It returns *ConflictError, with the deck untouched, if the deck's manifest
// Updated is not w.Manifest.Updated, and *BadNamesError if a file's name is
// not legal. On success w.Entries, w.Sums and w.Manifest.Updated are updated;
// w.Hash and w.Code are not: recomputing them is the caller's job.
func SaveFolder(w *Work, deckPath string, now time.Time) error {
	files, err := currentFiles(w)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	if err := checkNames(names); err != nil {
		return err
	}

	sums := map[string]string{}
	var entries []string
	err = atomicfile.Replace(deckPath, func(out io.Writer) error {
		zr, err := zip.OpenReader(deckPath)
		if err != nil {
			return err
		}
		defer zr.Close() // closed before the rename, which Windows needs

		var manifest []byte
		for _, f := range zr.File {
			if f.Name != brand.ManifestName {
				continue
			}
			if manifest != nil {
				return fmt.Errorf("more than one %s", brand.ManifestName)
			}
			if manifest, err = readLimited(f); err != nil {
				return err
			}
		}
		if manifest == nil {
			return fmt.Errorf("no %s", brand.ManifestName)
		}
		cur, err := deck.ReadManifest(&zr.Reader)
		if err != nil {
			return err
		}
		if !cur.Updated.Equal(w.Manifest.Updated) {
			return &ConflictError{Want: w.Manifest.Updated, Got: cur.Updated}
		}
		manifest, err = save.WithUpdated(manifest, now)
		if err != nil {
			return err
		}

		zw := zip.NewWriter(out)
		if err := zw.SetComment(zr.Comment); err != nil {
			return err
		}
		done := map[string]bool{}
		for _, f := range zr.File {
			if f.Name == brand.ManifestName {
				if err := addData(zw, f.Name, manifest, now); err != nil {
					return err
				}
				continue
			}
			dir, ok := files[f.Name]
			if !ok {
				continue // deleted
			}
			done[f.Name] = true
			entries = append(entries, f.Name)
			if dir {
				if err := zw.Copy(f); err != nil {
					return fmt.Errorf("%s: %w", f.Name, err)
				}
				continue
			}
			sum, err := writeFile(zw, f, w.Dir, f.Name, w.Sums[f.Name], now)
			if err != nil {
				return err
			}
			sums[f.Name] = sum
		}
		for _, name := range names {
			if done[name] || files[name] {
				continue
			}
			entries = append(entries, name)
			sum, err := writeFile(zw, nil, w.Dir, name, "", now)
			if err != nil {
				return err
			}
			sums[name] = sum
		}
		return zw.Close()
	})
	if err != nil {
		return err
	}
	w.Entries, w.Sums = entries, sums
	w.Manifest.Updated = now
	return nil
}

// currentFiles maps every entry name the work folder would give the deck to
// whether it is a folder. Folders are those of w.Entries that still exist.
func currentFiles(w *Work) (map[string]bool, error) {
	files := map[string]bool{}
	project, err := deck.ProjectFiles(w.Dir)
	if err != nil {
		return nil, err
	}
	for _, p := range project {
		files[p] = false
	}
	for _, name := range w.Entries {
		rel := deck.ExtractedPath(strings.TrimSuffix(name, "/"))
		st, err := os.Stat(filepath.Join(w.Dir, filepath.FromSlash(rel)))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		switch {
		case strings.HasSuffix(name, "/"):
			if st.IsDir() {
				files[name] = true
			}
		case strings.HasPrefix(name, vendorEntryPrefix):
			if st.Mode().IsRegular() {
				files[name] = false
			}
		}
	}
	return files, nil
}

// checkNames refuses names a deck cannot hold, and sets that collide.
func checkNames(names []string) error {
	var bad []string
	for _, n := range names {
		if n == brand.ManifestName || deck.CheckEntryName(n) != nil {
			bad = append(bad, n)
		}
	}
	if len(bad) > 0 {
		return &BadNamesError{Names: bad}
	}
	if _, _, err := deck.CheckEntryNames(names); err != nil {
		return fmt.Errorf("cannot save: %w", err)
	}
	return nil
}

// writeFile adds the work folder's file for entry name. If its sum is old and
// the old deck's entry f is given, that entry is copied raw. It returns the
// sum of what is in the deck.
func writeFile(zw *zip.Writer, f *zip.File, dir, name, old string, now time.Time) (string, error) {
	src, err := os.Open(filepath.Join(dir, filepath.FromSlash(deck.ExtractedPath(name))))
	if err != nil {
		return "", err
	}
	defer src.Close()
	h := sha256.New()
	if f != nil && old != "" {
		if _, err := io.Copy(h, src); err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		if sum := hex.EncodeToString(h.Sum(nil)); sum == old {
			if err := zw.Copy(f); err != nil {
				return "", fmt.Errorf("%s: %w", name, err)
			}
			return sum, nil
		}
		if _, err := src.Seek(0, io.SeekStart); err != nil {
			return "", err
		}
		h.Reset()
	}
	out, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: deck.EntryMethod(name), Modified: now})
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(io.MultiWriter(out, h), src); err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func addData(zw *zip.Writer, name string, data []byte, now time.Time) error {
	out, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: now})
	if err != nil {
		return err
	}
	_, err = out.Write(data)
	return err
}

func readLimited(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxManifestSize+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.Name, err)
	}
	if len(data) > maxManifestSize {
		return nil, fmt.Errorf("%s is too large", f.Name)
	}
	return data, nil
}
