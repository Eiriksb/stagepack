package save

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"stagepack/internal/deck"
)

// Status says whether the presenter's edits are in the deck file.
type Status int

const (
	Saved     Status = iota // everything is in the deck file
	Pending                 // "Saving…": an edit is not yet in the deck file
	Failed                  // writing the deck file failed; the edit is kept in the journal
	Throwaway               // edits are not saved (a deck opened without write-back)
)

func (s Status) String() string {
	switch s {
	case Saved:
		return "saved"
	case Pending:
		return "pending"
	case Failed:
		return "failed"
	case Throwaway:
		return "throwaway"
	}
	return fmt.Sprintf("Status(%d)", int(s))
}

// clock is the time source of a Watcher; tests use a fake one.
type clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// The timings of the global constraints.
const (
	DefaultPoll         = 500 * time.Millisecond // how often slides.md is checked
	DefaultJournalDelay = time.Second            // journal write after the last change
	DefaultQuietDelay   = 30 * time.Second       // deck rewrite after the last change

	settleEvery = 100 * time.Millisecond // FlushSettled: time between two reads that must agree
	settleFor   = 2 * time.Second        // FlushSettled: how long it waits for them at most
)

// WatcherConfig says what a Watcher watches and where edits go.
type WatcherConfig struct {
	SlidesPath string // the slides file in the work folder
	DeckPath   string // the deck file to write back into
	EntryName  string // the slides entry in the deck; default "slides.md"
	DeckID     string // the deck's manifest id, recorded in journal entries
	Key        string // the deck's journal key (JournalKey)
	Journal    Journal
	Base       time.Time // the manifest's "updated" when the deck was opened
	Clock      clock     // nil: the real clock

	Poll, JournalDelay, QuietDelay time.Duration // zero: the defaults

	// OnStatus is called when the status changes or a write fails. It is
	// called with the watcher's lock held: it must not call Flush or SetTarget.
	OnStatus func(Status, error)
}

// Watcher polls the slides file. A change goes to the journal JournalDelay
// after the last change, and into the deck file QuietDelay after it, or at
// once on Flush. A failed rewrite keeps the journal entry, reports Failed and
// is tried again on the next trigger: the next change's quiet period, or Flush.
type Watcher struct {
	slides, entry, deckID string
	poll                  time.Duration
	journalDelay          time.Duration
	quietDelay            time.Duration
	clock                 clock
	journal               Journal
	onStatus              func(Status, error)

	// put, rewrite and read are Journal.Put, RewriteEntry and os.ReadFile; tests wrap them.
	put     func(j Journal, key string, e Entry) error
	rewrite func(deckPath, name string, data []byte, now time.Time) error
	read    func(name string) ([]byte, error)

	// FlushSettled reads the slides file every settleEvery until two reads agree, for at most settleFor.
	settleEvery, settleFor time.Duration

	status atomic.Int32

	mu         sync.Mutex
	deckPath   string
	key        string
	base       time.Time
	haveHash   bool
	hash       [32]byte
	data       []byte    // the slides file as last seen
	changedAt  time.Time // when the last change was seen
	dirty      bool      // data is not in the deck file
	journaled  bool      // data is in the journal
	attempted  bool      // a rewrite of this change was tried by the quiet timer
	lastStatus Status
	lastErr    string
}

// NewWatcher makes a watcher. It takes the slides file as it is now as the
// state of the deck file. If the journal holds an entry for Key made on Base
// whose slides are that file (Recover applied it), that entry is pending and
// Run writes it into the deck file at its first poll.
func NewWatcher(cfg WatcherConfig) *Watcher {
	w := &Watcher{
		slides: cfg.SlidesPath, entry: cfg.EntryName, deckID: cfg.DeckID,
		poll: cfg.Poll, journalDelay: cfg.JournalDelay, quietDelay: cfg.QuietDelay,
		clock: cfg.Clock, journal: cfg.Journal, onStatus: cfg.OnStatus,
		put:         Journal.Put,
		rewrite:     RewriteEntry,
		read:        os.ReadFile,
		settleEvery: settleEvery, settleFor: settleFor,
		deckPath: cfg.DeckPath, key: cfg.Key, base: cfg.Base,
	}
	if w.entry == "" {
		w.entry = "slides.md"
	}
	if w.poll <= 0 {
		w.poll = DefaultPoll
	}
	if w.journalDelay <= 0 {
		w.journalDelay = DefaultJournalDelay
	}
	if w.quietDelay <= 0 {
		w.quietDelay = DefaultQuietDelay
	}
	if w.clock == nil {
		w.clock = realClock{}
	}
	if data, err := os.ReadFile(w.slides); err == nil {
		w.haveHash, w.hash, w.data = true, sha256.Sum256(data), data
		if w.key != "" {
			if e, ok, err := w.journal.Get(w.key); err == nil && ok &&
				e.BaseUpdated.Equal(w.base) && bytes.Equal(e.Slides, data) {
				w.dirty, w.journaled = true, true // changedAt zero: due at once
			}
		}
	}
	return w
}

// Status returns the current status. It is safe to call from any goroutine.
func (w *Watcher) Status() Status {
	return Status(w.status.Load())
}

// Run polls until ctx is cancelled. It does not write back on return: the
// stop path calls Flush.
func (w *Watcher) Run(ctx context.Context) {
	w.mu.Lock()
	if w.dirty {
		w.setStatus(Pending, nil)
	}
	w.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.clock.After(w.poll):
		}
		w.tick()
	}
}

func (w *Watcher) tick() {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.clock.Now()
	w.check(now)
	if !w.dirty {
		return
	}
	quiet := now.Sub(w.changedAt)
	if !w.journaled && quiet >= w.journalDelay {
		if err := w.journalNow(now); err != nil {
			w.setStatus(Failed, err)
		}
	}
	if !w.attempted && quiet >= w.quietDelay {
		w.attempted = true
		w.save(now)
	}
}

// Flush writes any pending edit into the deck file now, including one no poll
// has seen yet. It returns the write's error. It is safe to call while Run is
// running, and after Run has returned.
func (w *Watcher) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.clock.Now()
	w.check(now)
	return w.save(now)
}

// FlushSettled is Flush for the stop path. Slidev writes the slides file in
// place and may have been killed in the middle of a write, so the file is read
// until two reads settleEvery apart agree (for at most settleFor, then the last
// read is taken) before it is saved. Call it after Run has returned.
func (w *Watcher) FlushSettled() error {
	data, ok := w.settledRead()
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.clock.Now()
	if ok {
		w.take(now, data)
	}
	return w.save(now)
}

// settledRead reads the slides file until two reads in a row are the same and
// not empty. ok is false when no read gave anything.
func (w *Watcher) settledRead() (data []byte, ok bool) {
	var prev []byte
	deadline := time.Now().Add(w.settleFor)
	for {
		cur, err := w.read(w.slides)
		if err != nil || len(cur) == 0 {
			cur = nil
		} else {
			data, ok = cur, true
			if bytes.Equal(cur, prev) {
				return data, true
			}
		}
		prev = cur
		if !time.Now().Before(deadline) {
			return data, ok
		}
		time.Sleep(w.settleEvery)
	}
}

// SetTarget switches the deck file that later journal entries and rewrites go
// to (Save a copy). It reads the new deck's manifest for its id and "updated".
// A journal entry pending under the old key is moved to the new key: written
// under the new key first, then cleared under the old.
func (w *Watcher) SetTarget(deckPath, key string) error {
	m, err := readManifest(deckPath)
	if err != nil {
		return fmt.Errorf("save target: %w", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	e, ok, err := w.journal.Get(w.key)
	if err != nil {
		return fmt.Errorf("save target: %w", err)
	}
	if ok && key != w.key {
		e.DeckPath, e.DeckID, e.BaseUpdated = deckPath, m.ID, m.Updated
		if err := w.put(w.journal, key, e); err != nil {
			return fmt.Errorf("save target: %w", err)
		}
		if err := w.journal.Clear(w.key); err != nil {
			return fmt.Errorf("save target: %w", err)
		}
	}
	w.deckPath, w.key, w.deckID, w.base = deckPath, key, m.ID, m.Updated
	return nil
}

func readManifest(deckPath string) (deck.Manifest, error) {
	zr, err := zip.OpenReader(deckPath)
	if err != nil {
		return deck.Manifest{}, err
	}
	defer zr.Close()
	return deck.ReadManifest(&zr.Reader)
}

// check reads the slides file and records a change. An unreadable file (being
// replaced, say) is skipped until the next poll.
func (w *Watcher) check(now time.Time) {
	data, err := w.read(w.slides)
	if err != nil {
		return
	}
	w.take(now, data)
}

// take records data, the slides file as just read. An empty read is Slidev in
// the middle of writing the file, never an edit: it is ignored.
func (w *Watcher) take(now time.Time, data []byte) {
	if len(data) == 0 {
		return
	}
	sum := sha256.Sum256(data)
	if !w.haveHash {
		w.haveHash, w.hash, w.data = true, sum, data
		return
	}
	if sum == w.hash {
		return
	}
	w.hash, w.data, w.changedAt = sum, data, now
	w.dirty, w.journaled, w.attempted = true, false, false
	if w.lastStatus != Failed {
		w.setStatus(Pending, nil)
	}
}

func (w *Watcher) journalNow(now time.Time) error {
	e := Entry{DeckPath: w.deckPath, DeckID: w.deckID, BaseUpdated: w.base, Saved: now, Slides: w.data}
	if err := w.put(w.journal, w.key, e); err != nil {
		return err
	}
	w.journaled = true
	return nil
}

// save writes the pending edit into the deck file: journal first (so a
// failed rewrite still has it), then the rewrite, then the journal is cleared.
func (w *Watcher) save(now time.Time) error {
	if !w.dirty {
		return nil
	}
	if !w.journaled {
		if err := w.journalNow(now); err != nil {
			w.setStatus(Failed, err)
			// Go on: the deck file may still take it.
		}
	}
	now = now.Round(0).UTC()
	if err := w.rewrite(w.deckPath, w.entry, w.data, now); err != nil {
		w.setStatus(Failed, err)
		return err
	}
	w.base = now
	w.dirty = false
	if w.journaled {
		// A leftover entry has the old base, so Recover would set it aside, not apply it.
		w.journal.Clear(w.key)
	}
	w.journaled = false
	w.setStatus(Saved, nil)
	return nil
}

// setStatus records s and tells OnStatus when the status or the error changed.
func (w *Watcher) setStatus(s Status, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if s == w.lastStatus && msg == w.lastErr {
		return
	}
	w.lastStatus, w.lastErr = s, msg
	w.status.Store(int32(s))
	if w.onStatus != nil {
		w.onStatus(s, err)
	}
}
