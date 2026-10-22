package instance

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"stagepack/internal/brand"
)

func alwaysAlive(Record) bool { return true }
func neverAlive(Record) bool  { return false }

func mustRead(t *testing.T, home, deckPath string) Record {
	t.Helper()
	data, err := os.ReadFile(recordPath(home, deckPath))
	if err != nil {
		t.Fatal(err)
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// deadPID returns the PID of a process that has already exited.
func deadPID(t *testing.T) int {
	t.Helper()
	deadOnce.Do(func() {
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
		deadPIDValue = cmd.Process.Pid
	})
	return deadPIDValue
}

var (
	deadOnce     sync.Once
	deadPIDValue int
)

func TestInstanceClaimThenExisting(t *testing.T) {
	home := t.TempDir()
	deck := filepath.Join(t.TempDir(), "a"+brand.Ext)

	existing, h, err := Claim(home, deck, alwaysAlive)
	if err != nil {
		t.Fatal(err)
	}
	if existing != nil || h == nil {
		t.Fatalf("first claim: existing=%v release nil=%v", existing, h == nil)
	}
	if err := h.Update(4321, "http://127.0.0.1:4321", "/w"); err != nil {
		t.Fatal(err)
	}

	existing, h2, err := Claim(home, deck, alwaysAlive)
	if err != nil {
		t.Fatal(err)
	}
	if h2 != nil {
		t.Fatal("second claim must not return a handle")
	}
	if existing == nil || existing.PID != os.Getpid() || existing.Port != 4321 || existing.WorkDir != "/w" ||
		existing.DeckPath != deck {
		t.Fatalf("existing = %+v", existing)
	}

	h.Release()
	if _, err := os.Stat(recordPath(home, deck)); !os.IsNotExist(err) {
		t.Fatalf("record should be gone after release, stat err = %v", err)
	}
	// And it is claimable again.
	existing, h, err = Claim(home, deck, alwaysAlive)
	if err != nil || existing != nil || h == nil {
		t.Fatalf("claim after release: %v %v %v", existing, h == nil, err)
	}
	h.Release()
}

func TestInstanceStaleIsReplaced(t *testing.T) {
	home := t.TempDir()
	deck := filepath.Join(t.TempDir(), "a"+brand.Ext)
	if err := os.MkdirAll(filepath.Dir(recordPath(home, deck)), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := Record{PID: deadPID(t), Port: 1, DeckPath: deck, Token: "old"}
	data, _ := json.Marshal(stale)
	if err := os.WriteFile(recordPath(home, deck), data, 0o644); err != nil {
		t.Fatal(err)
	}

	existing, h, err := Claim(home, deck, neverAlive)
	if err != nil {
		t.Fatal(err)
	}
	if existing != nil || h == nil {
		t.Fatalf("stale record must be replaced: existing=%v", existing)
	}
	got := mustRead(t, home, deck)
	if got.PID != os.Getpid() || got.Token == "old" || got.Token == "" || got.Port != 0 || got.DeckPath != deck {
		t.Fatalf("record not rewritten for the new claimant: %+v", got)
	}
	h.Release()
}

func TestInstanceCorruptRecordIsReplaced(t *testing.T) {
	home := t.TempDir()
	deck := filepath.Join(t.TempDir(), "a"+brand.Ext)
	os.MkdirAll(filepath.Dir(recordPath(home, deck)), 0o755)
	os.WriteFile(recordPath(home, deck), []byte("{not json"), 0o644)
	existing, h, err := Claim(home, deck, alwaysAlive)
	if err != nil || existing != nil || h == nil {
		t.Fatalf("corrupt record: %v %v %v", existing, h == nil, err)
	}
	h.Release()
}

func TestInstanceConcurrentClaimExactlyOneWins(t *testing.T) {
	home := t.TempDir()
	deck := filepath.Join(t.TempDir(), "a"+brand.Ext)
	const n = 16
	var winners, losers atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			existing, h, err := Claim(home, deck, alwaysAlive)
			if err != nil {
				t.Error(err)
				return
			}
			if existing == nil {
				winners.Add(1)
			} else {
				losers.Add(1)
				if h != nil {
					t.Error("loser got a handle")
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	if winners.Load() != 1 || losers.Load() != n-1 {
		t.Fatalf("winners=%d losers=%d", winners.Load(), losers.Load())
	}
	if _, err := os.Stat(lockPath(home, deck)); !os.IsNotExist(err) {
		t.Fatalf("lock file left behind: %v", err)
	}
}

func TestInstanceStaleLockIsBroken(t *testing.T) {
	home := t.TempDir()
	deck := filepath.Join(t.TempDir(), "a"+brand.Ext)
	os.MkdirAll(filepath.Dir(lockPath(home, deck)), 0o755)
	lock := lockPath(home, deck)
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	existing, h, err := Claim(home, deck, alwaysAlive)
	if err != nil || existing != nil || h == nil {
		t.Fatalf("stale lock: %v %v %v", existing, h == nil, err)
	}
	h.Release()
}

func TestInstanceFreshLockIsWaitedFor(t *testing.T) {
	home := t.TempDir()
	deck := filepath.Join(t.TempDir(), "a"+brand.Ext)
	os.MkdirAll(filepath.Dir(lockPath(home, deck)), 0o755)
	lock := lockPath(home, deck)
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(60 * time.Millisecond)
		os.Remove(lock)
	}()
	existing, h, err := Claim(home, deck, alwaysAlive)
	if err != nil || existing != nil || h == nil {
		t.Fatalf("fresh lock: %v %v %v", existing, h == nil, err)
	}
	h.Release()
}

func TestInstanceFreshLockTimesOut(t *testing.T) {
	old := lockWait
	lockWait = 80 * time.Millisecond
	defer func() { lockWait = old }()
	home := t.TempDir()
	deck := filepath.Join(t.TempDir(), "a"+brand.Ext)
	os.MkdirAll(filepath.Dir(lockPath(home, deck)), 0o755)
	if err := os.WriteFile(lockPath(home, deck), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Claim(home, deck, alwaysAlive); err == nil {
		t.Fatal("want an error when the lock stays held")
	}
}

func TestInstanceReleaseLeavesNewerRecord(t *testing.T) {
	home := t.TempDir()
	deck := filepath.Join(t.TempDir(), "a"+brand.Ext)

	_, hA, err := Claim(home, deck, alwaysAlive)
	if err != nil {
		t.Fatal(err)
	}
	// A is judged dead, so B replaces it. Same PID here, so only the token tells them apart.
	_, hB, err := Claim(home, deck, neverAlive)
	if err != nil || hB == nil {
		t.Fatalf("B: %v", err)
	}
	b := mustRead(t, home, deck)

	hA.Release()
	if got := mustRead(t, home, deck); got.Token != b.Token {
		t.Fatalf("old release removed or changed the newer record: %+v", got)
	}
	hB.Release()
	if _, err := os.Stat(recordPath(home, deck)); !os.IsNotExist(err) {
		t.Fatalf("B's release should remove its own record: %v", err)
	}
}

func TestInstanceUpdateKeepsOwnership(t *testing.T) {
	home := t.TempDir()
	deck := filepath.Join(t.TempDir(), "a"+brand.Ext)
	_, h, err := Claim(home, deck, alwaysAlive)
	if err != nil {
		t.Fatal(err)
	}
	before := mustRead(t, home, deck)
	if err := h.Update(99, "http://127.0.0.1:99", ""); err != nil {
		t.Fatal(err)
	}
	after := mustRead(t, home, deck)
	if after.Port != 99 || after.Token != before.Token || after.PID != before.PID || after.DeckPath != deck {
		t.Fatalf("after Update: %+v (before %+v)", after, before)
	}
	h.Release()
	if _, err := os.Stat(recordPath(home, deck)); !os.IsNotExist(err) {
		t.Fatal("release after Update must still remove the record")
	}
}

func TestPathKey(t *testing.T) {
	tests := []struct{ in, goos, want string }{
		{`C:\Talks\A` + brand.Ext, "windows", `c:\talks\a` + brand.Ext},
		{`c:/talks/./x/../A` + strings.ToUpper(brand.Ext), "windows", `c:\talks\a` + brand.Ext},
		{`C:\Talks\\A` + brand.Ext, "windows", `c:\talks\a` + brand.Ext},
		{`\\Server\Share\A` + brand.Ext, "windows", `\\server\share\a` + brand.Ext},
		{"/home/a/Talks/../A" + brand.Ext, "linux", "/home/a/A" + brand.Ext},
		{"/home/a//A" + brand.Ext, "linux", "/home/a/A" + brand.Ext},
		{"/home/a/A" + brand.Ext, "linux", "/home/a/A" + brand.Ext},
	}
	for _, tc := range tests {
		if got := PathKey(tc.in, tc.goos); got != tc.want {
			t.Errorf("PathKey(%q, %s) = %q, want %q", tc.in, tc.goos, got, tc.want)
		}
	}
	// Linux paths are case-sensitive.
	if PathKey("/a/B", "linux") == PathKey("/a/b", "linux") {
		t.Error("linux keys must be case-sensitive")
	}
}

func TestRecordPathUsesHashOfKey(t *testing.T) {
	p := recordPath("/h", "/x/y"+brand.Ext)
	base := filepath.Base(p)
	if !strings.HasSuffix(base, ".json") || len(base) != 16+len(".json") {
		t.Fatalf("record file name = %q", base)
	}
	if filepath.Base(filepath.Dir(p)) != "instances" {
		t.Fatalf("record dir = %q", p)
	}
	if recordPath("/h", "/x/./y"+brand.Ext) != p {
		t.Fatal("equivalent paths must share a record")
	}
}

func TestAlive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404) // any answer counts
	}))
	defer srv.Close()
	self := os.Getpid()
	if !Alive(Record{PID: self, BaseURL: srv.URL}) {
		t.Error("running PID with answering server must be alive")
	}
	if Alive(Record{PID: deadPID(t), BaseURL: srv.URL}) {
		t.Error("dead PID must not be alive even if the port answers (PID reuse / other owner)")
	}
	if Alive(Record{PID: 0, BaseURL: srv.URL}) {
		t.Error("PID 0 is never alive")
	}

	// Port fallback when BaseURL is empty.
	p := srv.Listener.Addr().(*net.TCPAddr).Port
	if !Alive(Record{PID: self, Port: p, StartedAt: time.Now()}) {
		t.Error("Port fallback should reach the server")
	}

	srv.Close()
	if Alive(Record{PID: self, BaseURL: srv.URL, StartedAt: time.Now().Add(-time.Hour)}) {
		t.Error("closed server must not be alive")
	}
}

func TestAliveSlowAnswerLeansAlive(t *testing.T) {
	// A listener that accepts but never answers: a busy Slidev. With the PID running it is alive.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	old := probeTimeout
	probeTimeout = 150 * time.Millisecond
	defer func() { probeTimeout = old }()
	r := Record{PID: os.Getpid(), Port: ln.Addr().(*net.TCPAddr).Port, StartedAt: time.Now().Add(-time.Hour)}
	if !Alive(r) {
		t.Error("a timed-out probe with a running PID must count as alive")
	}
	if Alive(Record{PID: deadPID(t), Port: r.Port}) {
		t.Error("a timed-out probe with a dead PID is still dead")
	}
}

func TestAliveRefusedIsDead(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // nothing listens here any more
	if Alive(Record{PID: os.Getpid(), Port: port, StartedAt: time.Now().Add(-time.Hour)}) {
		t.Error("connection refused with a running PID (reused PID) must be dead")
	}
}

func TestAliveStartingInstance(t *testing.T) {
	// Claimed, Slidev not up yet: no address in the record. The PID decides while it is young.
	if !Alive(Record{PID: os.Getpid(), StartedAt: time.Now()}) {
		t.Error("a young record without an address must count as starting")
	}
	if Alive(Record{PID: os.Getpid(), StartedAt: time.Now().Add(-time.Hour)}) {
		t.Error("an old record without an address must be stale")
	}
	if Alive(Record{PID: deadPID(t), StartedAt: time.Now()}) {
		t.Error("a dead PID is never starting")
	}
}

func TestInstanceUpdateAfterOwnershipLost(t *testing.T) {
	home := t.TempDir()
	deck := filepath.Join(t.TempDir(), "a"+brand.Ext)
	_, hA, err := Claim(home, deck, alwaysAlive)
	if err != nil {
		t.Fatal(err)
	}
	// A is judged dead; B replaces it. Same PID here, so only the token tells them apart.
	_, hB, err := Claim(home, deck, neverAlive)
	if err != nil || hB == nil {
		t.Fatalf("B: %v", err)
	}
	b := mustRead(t, home, deck)

	if err := hA.Update(1234, "http://127.0.0.1:1234", "/a"); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("A's late Update = %v, want ErrNotOwner", err)
	}
	if got := mustRead(t, home, deck); got != b {
		t.Fatalf("A's late Update clobbered B's record: %+v", got)
	}
	if err := hB.Update(5678, "http://127.0.0.1:5678", "/b"); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, home, deck); got.Port != 5678 || got.Token != b.Token {
		t.Fatalf("B's Update: %+v", got)
	}
	hB.Release()
	hA.Release() // harmless
}

func TestInstanceUpdateAfterRecordGone(t *testing.T) {
	home := t.TempDir()
	deck := filepath.Join(t.TempDir(), "a"+brand.Ext)
	_, h, err := Claim(home, deck, alwaysAlive)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(recordPath(home, deck))
	if err := h.Update(1, "http://127.0.0.1:1", ""); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("Update on a missing record = %v, want ErrNotOwner", err)
	}
	if _, err := os.Stat(recordPath(home, deck)); !os.IsNotExist(err) {
		t.Fatal("Update must not recreate a missing record")
	}
}

func TestLockUnlockOnlyRemovesOwnLock(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "x.lock")
	unlock, err := acquire(lock)
	if err != nil {
		t.Fatal(err)
	}
	// Someone broke our (supposedly stale) lock and holds a new one.
	if err := os.WriteFile(lock, []byte("someone-else"), 0o644); err != nil {
		t.Fatal(err)
	}
	unlock()
	if data, err := os.ReadFile(lock); err != nil || string(data) != "someone-else" {
		t.Fatalf("a late unlock deleted another holder's lock: %q %v", data, err)
	}
	// And a normal unlock removes it.
	os.Remove(lock)
	unlock, err = acquire(lock)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatal("unlock left the lock behind")
	}
}

func TestInstanceManyWaitersBreakOneStaleLock(t *testing.T) {
	home := t.TempDir()
	deck := filepath.Join(t.TempDir(), "a"+brand.Ext)
	os.MkdirAll(filepath.Dir(lockPath(home, deck)), 0o755)
	lock := lockPath(home, deck)
	if err := os.WriteFile(lock, []byte("crashed"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	os.Chtimes(lock, old, old)

	const n = 16
	var winners atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			existing, _, err := Claim(home, deck, alwaysAlive)
			if err != nil {
				t.Error(err)
				return
			}
			if existing == nil {
				winners.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("winners = %d, want exactly 1", winners.Load())
	}
	entries, _ := os.ReadDir(filepath.Dir(lock))
	for _, e := range entries {
		if strings.Contains(e.Name(), ".stale-") || strings.HasSuffix(e.Name(), ".breaking") || strings.HasSuffix(e.Name(), ".lock") {
			t.Errorf("leftover %s", e.Name())
		}
	}
}

func TestBreakLockLeavesLiveLock(t *testing.T) {
	// A waiter that stat'ed an old lock must not remove a fresh one (re-checked under the guard).
	lock := filepath.Join(t.TempDir(), "x.lock")
	if err := os.WriteFile(lock, []byte("live"), 0o644); err != nil {
		t.Fatal(err)
	}
	breakLock(lock)
	if data, err := os.ReadFile(lock); err != nil || string(data) != "live" {
		t.Fatalf("live lock removed: %q %v", data, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(lock))
	if len(entries) != 1 {
		t.Fatalf("leftovers: %v", entries)
	}
}

func TestList(t *testing.T) {
	home := t.TempDir()
	if got := List(home); len(got) != 0 {
		t.Fatalf("List of an empty home = %v", got)
	}
	a := filepath.Join(t.TempDir(), "a"+brand.Ext)
	b := filepath.Join(t.TempDir(), "b"+brand.Ext)
	for _, p := range []string{a, b} {
		if _, _, err := Claim(home, p, neverAlive); err != nil {
			t.Fatal(err)
		}
	}
	// A corrupt record and a stray temp file are skipped.
	os.WriteFile(filepath.Join(home, "instances", "0000000000000000.json"), []byte("{"), 0o644)
	os.WriteFile(filepath.Join(home, "instances", "x.json.123.tmp"), []byte("{}"), 0o644)
	got := List(home)
	if len(got) != 2 {
		t.Fatalf("List = %+v, want 2 records", got)
	}
	seen := map[string]bool{}
	for _, r := range got {
		seen[r.DeckPath] = true
	}
	if !seen[a] || !seen[b] {
		t.Errorf("List = %+v", got)
	}
}
