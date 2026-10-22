// Package trust remembers which talks the person has allowed to run their
// code, so the same talk is asked about once, and a changed one again.
package trust

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// FileName is the list's name in the viewer's folder.
const FileName = "trusted.json"

// Store is the list of trusted talks in one file.
type Store struct {
	Path string // <home>/FileName
}

// entry is one talk: the version (workspace.Work.Hash) that was allowed.
type entry struct {
	Hash    string    `json:"hash"`
	Title   string    `json:"title,omitempty"` // for whoever reads the file
	Trusted time.Time `json:"trusted"`
}

type list struct {
	Decks map[string]entry `json:"decks"` // by deck ID
}

// load reads the list. A missing or unreadable file is an empty list: the
// worst that does is ask again.
func (s Store) load() list {
	var l list
	if data, err := os.ReadFile(s.Path); err == nil {
		if json.Unmarshal(data, &l) != nil {
			l = list{}
		}
	}
	if l.Decks == nil {
		l.Decks = map[string]entry{}
	}
	return l
}

// Trusted reports whether the talk id was allowed in exactly this version.
func (s Store) Trusted(id, hash string) bool {
	if hash == "" {
		return false
	}
	e, ok := s.load().Decks[id]
	return ok && e.Hash == hash
}

// Remember records that the talk id may run its code in this version. It
// replaces what was remembered for an earlier version of the same talk. The
// file is written beside and renamed over, so it is never half written; two
// viewers remembering at the same moment may lose one of the two, which only
// means that talk is asked about again.
func (s Store) Remember(id, hash, title string) error {
	if hash == "" {
		return errors.New("trust: no hash")
	}
	l := s.load()
	l.Decks[id] = entry{Hash: hash, Title: title, Trusted: time.Now().UTC().Truncate(time.Second)}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), FileName+".*.tmp")
	if err != nil {
		return err
	}
	_, err = tmp.Write(append(data, '\n'))
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), s.Path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}
