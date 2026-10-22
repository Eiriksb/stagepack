package save

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"stagepack/internal/brand"
	"stagepack/internal/deck"
	"stagepack/internal/pack"
)

const fixture = "../../testdata/deck"

// packFixture packs the fixture deck into a new temp folder and returns its path and manifest.
func packFixture(t *testing.T) (string, deck.Manifest) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "talk"+brand.Ext)
	m, err := pack.Pack(pack.Options{Dir: fixture, Out: out, Warn: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	return out, m
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// entryText returns the contents of one entry of the deck.
func entryText(t *testing.T, deckPath, name string) string {
	t.Helper()
	zr, err := zip.OpenReader(deckPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	t.Fatalf("%s has no entry %s", deckPath, name)
	return ""
}

func manifestOf(t *testing.T, deckPath string) deck.Manifest {
	t.Helper()
	zr, err := zip.OpenReader(deckPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	m, err := deck.ReadManifest(&zr.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// fakeClock is a clock driven by the test. Exactly one goroutine (the
// watcher's Run) waits on it, so "a waiter is registered" means the watcher is
// idle between polls.
type fakeClock struct {
	mu      sync.Mutex
	cond    *sync.Cond
	start   time.Time
	now     time.Time
	waiters []fakeWaiter
}

type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock {
	start := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	c := &fakeClock{start: start, now: start}
	c.cond = sync.NewCond(&c.mu)
	return c
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, fakeWaiter{at: c.now.Add(d), ch: ch})
	c.cond.Broadcast()
	return ch
}

// elapsed is the fake time since the clock started.
func (c *fakeClock) elapsed() time.Duration {
	return c.Now().Sub(c.start)
}

// runTo moves the fake time to start+d, firing the watcher's polls on the way
// and waiting for each poll to finish before going on.
func (c *fakeClock) runTo(d time.Duration) {
	target := c.start.Add(d)
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		for len(c.waiters) == 0 {
			c.cond.Wait()
		}
		next := 0
		for i, w := range c.waiters {
			if w.at.Before(c.waiters[next].at) {
				next = i
			}
		}
		w := c.waiters[next]
		if w.at.After(target) {
			if target.After(c.now) {
				c.now = target
			}
			return
		}
		c.waiters = append(c.waiters[:next], c.waiters[next+1:]...)
		c.now = w.at
		w.ch <- w.at
	}
}

// statusLog records OnStatus calls with the fake time they happened at.
type statusLog struct {
	mu    sync.Mutex
	clock *fakeClock
	list  []Status
	errs  []error
	at    []time.Duration
}

func (l *statusLog) record(s Status, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.list = append(l.list, s)
	l.errs = append(l.errs, err)
	l.at = append(l.at, l.clock.elapsed())
}

func (l *statusLog) statuses() []Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Status(nil), l.list...)
}

func (l *statusLog) last() (Status, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.list) == 0 {
		return -1, nil
	}
	return l.list[len(l.list)-1], l.errs[len(l.errs)-1]
}
