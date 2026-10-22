package save

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"stagepack/internal/deck"

	"stagepack/internal/brand"
)

// rig is a packed deck, its work slides, a journal and a watcher on a fake clock.
type rig struct {
	t        *testing.T
	deckPath string
	m        deck.Manifest
	work     string
	j        Journal
	key      string
	clock    *fakeClock
	log      *statusLog
	w        *Watcher

	mu       sync.Mutex
	puts     []time.Duration
	rewrites []time.Duration

	cancel context.CancelFunc
	done   chan struct{}
}

func newRig(t *testing.T) *rig {
	t.Helper()
	deckPath, m := packFixture(t)
	r := &rig{t: t, deckPath: deckPath, m: m, clock: newFakeClock()}
	r.work = filepath.Join(t.TempDir(), "slides.md")
	writeFile(t, r.work, entryText(t, deckPath, "slides.md"))
	r.j = Journal{Dir: filepath.Join(t.TempDir(), "journal")}
	r.key = JournalKey(m.ID, deckPath)
	r.log = &statusLog{clock: r.clock}
	return r
}

// start makes the watcher and runs it in the background.
func (r *rig) start() {
	r.w = NewWatcher(WatcherConfig{
		SlidesPath: r.work, DeckPath: r.deckPath, EntryName: r.m.Entry, DeckID: r.m.ID,
		Key: r.key, Journal: r.j, Base: r.m.Updated, Clock: r.clock, OnStatus: r.log.record,
	})
	put, rewrite := r.w.put, r.w.rewrite
	r.w.put = func(j Journal, key string, e Entry) error {
		r.mu.Lock()
		r.puts = append(r.puts, r.clock.elapsed())
		r.mu.Unlock()
		return put(j, key, e)
	}
	r.w.rewrite = func(deckPath, name string, data []byte, now time.Time) error {
		r.mu.Lock()
		r.rewrites = append(r.rewrites, r.clock.elapsed())
		r.mu.Unlock()
		return rewrite(deckPath, name, data, now)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan struct{})
	go func() {
		defer close(r.done)
		r.w.Run(ctx)
	}()
	r.t.Cleanup(r.stop)
}

func (r *rig) stop() {
	if r.cancel != nil {
		r.cancel()
		<-r.done
		r.cancel = nil
	}
}

func (r *rig) counts() (puts, rewrites []time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Duration(nil), r.puts...), append([]time.Duration(nil), r.rewrites...)
}

func (r *rig) edit(s string) { writeFile(r.t, r.work, s) }

func TestWatcherDebounce(t *testing.T) {
	r := newRig(t)
	r.start()
	const final = "# edit 3\n"
	r.edit("# edit 1\n") // t=0
	r.clock.runTo(400 * time.Millisecond)
	r.edit("# edit 2\n")
	r.clock.runTo(800 * time.Millisecond)
	r.edit(final)

	for d := 900 * time.Millisecond; d <= 35*time.Second; d += 100 * time.Millisecond {
		r.clock.runTo(d)
		puts, rewrites := r.counts()
		if len(rewrites) > 0 && rewrites[0] < 30800*time.Millisecond {
			t.Fatalf("rewrite at %v, before 30.8 s", rewrites[0])
		}
		if len(puts) > 0 && puts[0] < 1800*time.Millisecond {
			t.Fatalf("journal put at %v, before 1.8 s", puts[0])
		}
		if d == 29*time.Second {
			e, ok, err := r.j.Get(r.key)
			if err != nil || !ok || string(e.Slides) != final {
				t.Fatalf("journal at 29 s: %q, %v, %v", e.Slides, ok, err)
			}
			if !e.BaseUpdated.Equal(r.m.Updated) || e.DeckPath != r.deckPath || e.DeckID != r.m.ID {
				t.Errorf("journal entry %+v", e)
			}
			if got := entryText(t, r.deckPath, "slides.md"); got == final {
				t.Fatal("deck rewritten before the quiet period")
			}
		}
	}
	puts, rewrites := r.counts()
	if len(puts) != 1 || puts[0] > 2300*time.Millisecond {
		t.Errorf("journal puts at %v, want one at about 1.8 s", puts)
	}
	if len(rewrites) != 1 || rewrites[0] > 31300*time.Millisecond {
		t.Errorf("rewrites at %v, want one at about 30.8 s", rewrites)
	}
	if got := entryText(t, r.deckPath, "slides.md"); got != final {
		t.Errorf("deck slides.md = %q, want %q", got, final)
	}
	if _, ok, _ := r.j.Get(r.key); ok {
		t.Error("journal entry not cleared after the rewrite")
	}
	if got := r.log.statuses(); !reflect.DeepEqual(got, []Status{Pending, Saved}) {
		t.Errorf("statuses %v, want [pending saved]", got)
	}
	if r.w.Status() != Saved {
		t.Errorf("Status() = %v", r.w.Status())
	}
}

func TestFlushOnStop(t *testing.T) {
	r := newRig(t)
	r.start()
	r.edit("# flushed\n")
	r.clock.runTo(2 * time.Second)
	if err := r.w.Flush(); err != nil {
		t.Fatal(err)
	}
	_, rewrites := r.counts()
	if len(rewrites) != 1 || rewrites[0] != 2*time.Second {
		t.Fatalf("rewrites at %v, want one at 2 s", rewrites)
	}
	if got := entryText(t, r.deckPath, "slides.md"); got != "# flushed\n" {
		t.Errorf("deck slides.md = %q", got)
	}
	if _, ok, _ := r.j.Get(r.key); ok {
		t.Error("journal entry not cleared")
	}
	if s, _ := r.log.last(); s != Saved {
		t.Errorf("status %v, want saved", s)
	}
	// Nothing pending: a second Flush does not rewrite.
	if err := r.w.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, rewrites := r.counts(); len(rewrites) != 1 {
		t.Errorf("second Flush rewrote: %v", rewrites)
	}
}

func TestFlushSeesUnpolledEdit(t *testing.T) {
	r := newRig(t)
	r.start()
	r.clock.runTo(100 * time.Millisecond)
	r.edit("# just now\n") // no poll has seen this yet
	if err := r.w.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := entryText(t, r.deckPath, "slides.md"); got != "# just now\n" {
		t.Errorf("deck slides.md = %q", got)
	}
}

func TestRunStopsOnCancelWithoutRewrite(t *testing.T) {
	r := newRig(t)
	r.start()
	orig := readFile(t, r.deckPath)
	r.edit("# pending\n")
	r.clock.runTo(time.Second)
	r.stop() // returns: Run honours the cancel
	if !bytes.Equal(readFile(t, r.deckPath), orig) {
		t.Error("cancel rewrote the deck; only Flush may")
	}
	if r.w.Status() != Pending {
		t.Errorf("Status() = %v, want pending", r.w.Status())
	}
}

func TestBaseFollowsRewrite(t *testing.T) {
	r := newRig(t)
	r.start()
	r.edit("# one\n")
	r.clock.runTo(time.Second)
	if err := r.w.Flush(); err != nil {
		t.Fatal(err)
	}
	updated := manifestOf(t, r.deckPath).Updated
	if updated.Equal(r.m.Updated) {
		t.Fatal("manifest updated not changed by the rewrite")
	}
	r.edit("# two\n")
	r.clock.runTo(3 * time.Second)
	e, ok, err := r.j.Get(r.key)
	if err != nil || !ok {
		t.Fatalf("no journal entry: %v", err)
	}
	if !e.BaseUpdated.Equal(updated) {
		t.Errorf("BaseUpdated %v, want the last rewrite's %v", e.BaseUpdated, updated)
	}
	// A crash now recovers against the deck as it is on disk.
	applied, _, err := Recover(r.j, r.key, manifestOf(t, r.deckPath), filepath.Join(t.TempDir(), "s.md"))
	if err != nil || !applied {
		t.Errorf("Recover = %v, %v", applied, err)
	}
}

func skipChmod(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("chmod does not make a folder read-only on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores folder permissions")
	}
}

func TestWriteBackReadOnlyDirKeepsJournal(t *testing.T) {
	skipChmod(t)
	r := newRig(t)
	orig := readFile(t, r.deckPath)
	dir := filepath.Dir(r.deckPath)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	r.start()
	r.edit("# keep me\n")
	r.clock.runTo(32 * time.Second)

	if s, err := r.log.last(); s != Failed || err == nil {
		t.Fatalf("status %v (%v), want failed with an error", s, err)
	}
	if r.w.Status() != Failed {
		t.Errorf("Status() = %v", r.w.Status())
	}
	e, ok, err := r.j.Get(r.key)
	if err != nil || !ok || string(e.Slides) != "# keep me\n" {
		t.Fatalf("journal entry: %q, %v, %v", e.Slides, ok, err)
	}
	if !bytes.Equal(readFile(t, r.deckPath), orig) {
		t.Error("deck changed")
	}
	// No retry every poll: the next trigger is a change or Flush.
	_, rewrites := r.counts()
	r.clock.runTo(70 * time.Second)
	if _, again := r.counts(); len(again) != len(rewrites) {
		t.Errorf("retried without a trigger: %v", again)
	}
	if err := r.w.Flush(); err == nil {
		t.Error("Flush into a read-only folder returned nil")
	}
	if s, _ := r.log.last(); s != Failed {
		t.Errorf("status %v after a failed Flush", s)
	}

	// The folder becomes writable: the next trigger saves it.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := r.w.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := entryText(t, r.deckPath, "slides.md"); got != "# keep me\n" {
		t.Errorf("deck slides.md = %q", got)
	}
	if _, ok, _ := r.j.Get(r.key); ok {
		t.Error("journal entry not cleared")
	}
	if s, _ := r.log.last(); s != Saved {
		t.Errorf("status %v, want saved", s)
	}
}

func TestFailedRewriteRetriesAfterNextChange(t *testing.T) {
	skipChmod(t)
	r := newRig(t)
	dir := filepath.Dir(r.deckPath)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	r.start()
	r.edit("# a\n")
	r.clock.runTo(32 * time.Second)
	if s, _ := r.log.last(); s != Failed {
		t.Fatalf("status %v, want failed", s)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	r.edit("# b\n")
	r.clock.runTo(40 * time.Second)
	if s, _ := r.log.last(); s != Failed {
		t.Errorf("status %v before the rewrite, want still failed", s)
	}
	r.clock.runTo(64 * time.Second)
	if got := entryText(t, r.deckPath, "slides.md"); got != "# b\n" {
		t.Errorf("deck slides.md = %q", got)
	}
	if s, _ := r.log.last(); s != Saved {
		t.Errorf("status %v, want saved", s)
	}
}

func TestWatcherWritesRecoveredEntry(t *testing.T) {
	r := newRig(t)
	if err := r.j.Put(r.key, Entry{DeckPath: r.deckPath, DeckID: r.m.ID, BaseUpdated: r.m.Updated, Slides: []byte("# crashed edit\n")}); err != nil {
		t.Fatal(err)
	}
	applied, _, err := Recover(r.j, r.key, r.m, r.work)
	if err != nil || !applied {
		t.Fatalf("Recover = %v, %v", applied, err)
	}
	r.start()
	r.clock.runTo(time.Second)
	if got := entryText(t, r.deckPath, "slides.md"); got != "# crashed edit\n" {
		t.Errorf("deck slides.md = %q", got)
	}
	if _, ok, _ := r.j.Get(r.key); ok {
		t.Error("journal entry not cleared")
	}
	if got := r.log.statuses(); !reflect.DeepEqual(got, []Status{Pending, Saved}) {
		t.Errorf("statuses %v", got)
	}
}

func TestSetTargetMovesPending(t *testing.T) {
	r := newRig(t)
	r.start()
	r.edit("# for the copy\n")
	r.clock.runTo(2500 * time.Millisecond) // journaled, not yet rewritten
	if _, ok, _ := r.j.Get(r.key); !ok {
		t.Fatal("no journal entry to move")
	}
	orig := readFile(t, r.deckPath)
	copyPath := filepath.Join(t.TempDir(), "copy"+brand.Ext)
	if err := os.WriteFile(copyPath, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	newKey := JournalKey(r.m.ID, copyPath)

	if err := r.w.SetTarget(copyPath, newKey); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := r.j.Get(r.key); ok {
		t.Error("entry still under the old key")
	}
	e, ok, err := r.j.Get(newKey)
	if err != nil || !ok {
		t.Fatalf("no entry under the new key: %v", err)
	}
	if e.DeckPath != copyPath || string(e.Slides) != "# for the copy\n" || !e.BaseUpdated.Equal(r.m.Updated) {
		t.Errorf("moved entry %+v", e)
	}

	if err := r.w.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := entryText(t, copyPath, "slides.md"); got != "# for the copy\n" {
		t.Errorf("copy slides.md = %q", got)
	}
	if !bytes.Equal(readFile(t, r.deckPath), orig) {
		t.Error("the original was rewritten after SetTarget")
	}
	if _, ok, _ := r.j.Get(newKey); ok {
		t.Error("new key not cleared after the rewrite")
	}
}

func TestSetTargetBadDeck(t *testing.T) {
	r := newRig(t)
	r.start()
	bad := filepath.Join(t.TempDir(), "nope"+brand.Ext)
	if err := r.w.SetTarget(bad, JournalKey(r.m.ID, bad)); err == nil {
		t.Fatal("SetTarget to a missing deck = nil")
	}
	r.edit("# still here\n")
	if err := r.w.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := entryText(t, r.deckPath, "slides.md"); got != "# still here\n" {
		t.Errorf("target changed by a failed SetTarget: %q", got)
	}
}

// Flush from another goroutine while Run polls (run with -race).
func TestFlushConcurrentWithRun(t *testing.T) {
	r := newRig(t)
	r.start()
	stop := make(chan struct{})
	driven := make(chan struct{})
	go func() {
		defer close(driven)
		for d := 100 * time.Millisecond; ; d += 100 * time.Millisecond {
			select {
			case <-stop:
				return
			default:
			}
			r.clock.runTo(d)
		}
	}()
	for i := 0; i < 20; i++ {
		r.edit("# edit " + string(rune('a'+i)) + "\n")
		if err := r.w.Flush(); err != nil {
			t.Fatal(err)
		}
		_ = r.w.Status()
	}
	close(stop)
	<-driven
	if got := entryText(t, r.deckPath, "slides.md"); got != "# edit t\n" {
		t.Errorf("deck slides.md = %q", got)
	}
}

func TestStatusString(t *testing.T) {
	for s, want := range map[Status]string{Saved: "saved", Pending: "pending", Failed: "failed", Throwaway: "throwaway"} {
		if s.String() != want {
			t.Errorf("%d.String() = %q, want %q", s, s.String(), want)
		}
	}
}

func TestWatcherDefaults(t *testing.T) {
	w := NewWatcher(WatcherConfig{SlidesPath: "x"})
	if w.poll != 500*time.Millisecond || w.journalDelay != time.Second || w.quietDelay != 30*time.Second {
		t.Errorf("defaults %v %v %v", w.poll, w.journalDelay, w.quietDelay)
	}
}

// Slidev writes the slides file in place, so a read can catch it empty: that is never an edit.
func TestEmptyReadIgnoredByTick(t *testing.T) {
	r := newRig(t)
	r.start()
	r.edit("# real edit\n")
	r.clock.runTo(2 * time.Second) // journaled
	r.edit("")                     // caught mid-write
	r.clock.runTo(5 * time.Second)
	if e, ok, err := r.j.Get(r.key); err != nil || !ok || string(e.Slides) != "# real edit\n" {
		t.Errorf("journal = %q, %v, %v; want the real edit", e.Slides, ok, err)
	}
	r.clock.runTo(40 * time.Second)
	if got := entryText(t, r.deckPath, "slides.md"); got != "# real edit\n" {
		t.Errorf("deck slides.md = %q, want the real edit", got)
	}
}

func TestEmptyReadIgnoredByFlush(t *testing.T) {
	r := newRig(t)
	r.start()
	orig := readFile(t, r.deckPath)
	r.edit("")
	if err := r.w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := r.w.FlushSettled(); err != nil {
		t.Fatal(err)
	}
	if _, rewrites := r.counts(); len(rewrites) != 0 || !bytes.Equal(readFile(t, r.deckPath), orig) {
		t.Errorf("an empty slides file was written into the deck (rewrites %v)", rewrites)
	}
	if _, ok, _ := r.j.Get(r.key); ok {
		t.Error("an empty slides file was journaled")
	}
	if s := r.w.Status(); s != Saved {
		t.Errorf("Status() = %v, want saved", s)
	}
}

// fakeReads makes the watcher's reads return each of seq in turn, then the last one for good.
func fakeReads(w *Watcher, seq ...string) *atomic.Int32 {
	var n atomic.Int32
	w.read = func(string) ([]byte, error) {
		i := int(n.Add(1)) - 1
		if i >= len(seq) {
			i = len(seq) - 1
		}
		return []byte(seq[i]), nil
	}
	w.settleEvery = time.Millisecond
	return &n
}

func TestFlushSettledWaitsForStableFile(t *testing.T) {
	r := newRig(t)
	r.start()
	reads := fakeReads(r.w, "# half wri", "", "# fully written\n")
	if err := r.w.FlushSettled(); err != nil {
		t.Fatal(err)
	}
	if got := entryText(t, r.deckPath, "slides.md"); got != "# fully written\n" {
		t.Errorf("deck slides.md = %q, want the settled content", got)
	}
	if n := reads.Load(); n < 4 {
		t.Errorf("%d reads, want at least 4 (two equal ones in a row at the end)", n)
	}
}

func TestFlushSettledGivesUpAfterMax(t *testing.T) {
	r := newRig(t)
	r.start()
	var n atomic.Int32
	r.w.read = func(string) ([]byte, error) { return []byte(fmt.Sprintf("# version %d\n", n.Add(1))), nil }
	r.w.settleEvery, r.w.settleFor = time.Millisecond, 50*time.Millisecond
	start := time.Now()
	if err := r.w.FlushSettled(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("FlushSettled took %v", d)
	}
	if got := entryText(t, r.deckPath, "slides.md"); !strings.HasPrefix(got, "# version ") {
		t.Errorf("deck slides.md = %q, want the last read", got)
	}
}
