// Package save keeps a presenter's note edits: a journal entry within about a
// second of each change, and a safe rewrite of the deck file after a quiet
// spell and on stop.
package save

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"stagepack/internal/deck"
)

// Entry is one deck's slides file that is not yet (known to be) in the deck file.
type Entry struct {
	DeckPath    string    `json:"deckPath"`
	DeckID      string    `json:"deckId"`
	BaseUpdated time.Time `json:"baseUpdated"` // the deck's manifest "updated" the edit was made on
	Saved       time.Time `json:"saved"`       // when the entry was written
	Slides      []byte    `json:"slides"`
}

// Journal is a folder of entries, one per deck, stored as <Dir>/<key>.json.
// The folder is created on demand.
type Journal struct {
	Dir string
}

// JournalKey names a deck's journal entry: the deck's ID and a hash of where
// the file is, so two copies of one deck have separate entries. An ID that is
// not plain letters, digits and dashes (it comes from the deck, so it is not
// trusted as a file name) is replaced by a hash of it.
func JournalKey(deckID, absPath string) string {
	if !plainKey(deckID) || len(deckID) > 64 {
		deckID = hashHex(deckID)
	}
	return deckID + "-" + hashHex(absPath)
}

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

// plainKey reports whether s is non-empty and only letters, digits, '-' and '_'.
func plainKey(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		ok := r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			return false
		}
	}
	return true
}

func (j Journal) path(key string) (string, error) {
	if !plainKey(key) {
		return "", fmt.Errorf("journal: bad key %q", key)
	}
	if j.Dir == "" {
		return "", errors.New("journal: no folder")
	}
	return filepath.Join(j.Dir, key+".json"), nil
}

// Put stores e under key, replacing any entry there. It writes a temp file
// next to it and renames it, so a crash leaves either the old or the new entry.
func (j Journal) Put(key string, e Entry) error {
	p, err := j.path(key)
	if err != nil {
		return err
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(j.Dir, 0o755); err != nil {
		return fmt.Errorf("journal: %w", err)
	}
	if err := writeFileAtomic(p, data, 0o644); err != nil {
		return fmt.Errorf("journal: %w", err)
	}
	return nil
}

// Get returns the entry under key; ok is false if there is none.
func (j Journal) Get(key string) (e Entry, ok bool, err error) {
	p, err := j.path(key)
	if err != nil {
		return Entry{}, false, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("journal: %w", err)
	}
	if err := json.Unmarshal(data, &e); err != nil {
		return Entry{}, false, fmt.Errorf("journal: %s: %w", p, err)
	}
	return e, true, nil
}

// Clear removes the entry under key. A missing entry is not an error.
func (j Journal) Clear(key string) error {
	p, err := j.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("journal: %w", err)
	}
	return nil
}

// Recover looks for an entry left by a crash. If its edit was made on the
// deck as it is now (BaseUpdated equals m.Updated, same deck ID), it writes the
// journaled slides to workSlides and returns applied. The entry stays in the
// journal until the deck file has it: a Watcher on the same key writes it
// back. If the deck changed since the edit (saved elsewhere, say), the entry
// is not applied: it is moved aside, so new edits do not overwrite it, and
// kept is the path it was moved to.
func Recover(j Journal, key string, m deck.Manifest, workSlides string) (applied bool, kept string, err error) {
	e, ok, err := j.Get(key)
	if err != nil || !ok {
		return false, "", err
	}
	if e.BaseUpdated.Equal(m.Updated) && (e.DeckID == "" || e.DeckID == m.ID) {
		if err := writeFileAtomic(workSlides, e.Slides, 0o644); err != nil {
			return false, "", fmt.Errorf("recover: %w", err)
		}
		return true, "", nil
	}
	kept, err = SetAside(j, key)
	if err != nil {
		return false, "", fmt.Errorf("recover: %w", err)
	}
	return false, kept, nil
}

// SetAside moves the entry under key, readable or not, to
// <key>.kept-<time>.json in the journal folder, so that new edits cannot
// overwrite it, and returns that path. With no entry it returns "", nil.
func SetAside(j Journal, key string) (kept string, err error) {
	src, err := j.path(key)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(src); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	kept = filepath.Join(j.Dir, fmt.Sprintf("%s.kept-%s.json", key, time.Now().UTC().Format("20060102T150405.000000000Z")))
	if err := replaceFile(src, kept); err != nil {
		return "", fmt.Errorf("keep %s: %w", src, err)
	}
	return kept, nil
}

// writeFileAtomic writes data to a temp file in path's folder, syncs it and
// renames it over path.
func writeFileAtomic(path string, data []byte, perm os.FileMode) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Chmod(perm); err != nil && runtime.GOOS != "windows" {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = replaceFile(tmp, path); err != nil {
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}

// Renaming over a file on Windows fails for a moment while a virus scanner or
// the indexer has it open, so the rename is retried there.
var (
	rename         = os.Rename
	retryRename    = runtime.GOOS == "windows"
	renameAttempts = 5
	renameWait     = 250 * time.Millisecond // between attempts: about 1 s in all
)

// replaceFile renames src over dst, retrying on Windows.
func replaceFile(src, dst string) error {
	var err error
	for i := 0; i < renameAttempts; i++ {
		if i > 0 {
			time.Sleep(renameWait)
		}
		if err = rename(src, dst); err == nil || !retryRename {
			return err
		}
	}
	return err
}

// syncDir makes a rename in dir durable where the OS supports it (not Windows).
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}
