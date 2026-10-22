// Package instance makes sure one deck is open in one viewer process at a time, and
// recognises decks opened from a throwaway copy (mail attachments, temp folders).
//
// Two processes on one deck would share a journal key (see internal/save) and race their
// write-backs, so the claim is load-bearing for data safety, not only a convenience.
//
// A claim is a small JSON record at <home>/instances/<hash>.json that lives as long as the
// instance. A short-lived <hash>.lock file serialises the check-and-write of that record.
package instance

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Record is what an instance leaves behind so that a second launch can find it.
type Record struct {
	PID      int    `json:"pid"`
	Port     int    `json:"port"`
	BaseURL  string `json:"baseUrl,omitempty"` // as answered by Slidev, e.g. http://127.0.0.1:41234
	DeckPath string `json:"deckPath"`
	WorkDir  string `json:"workDir"`
	// Token tells two claimants with the same PID apart, and lets Release leave a newer
	// instance's record alone.
	Token string `json:"token"`
	// StartedAt bounds how long a record without an address counts as "still starting".
	StartedAt time.Time `json:"startedAt"`
}

var (
	// lockWait is how long a lock is waited for before giving up. It exceeds staleLock so that
	// a lock left by a crash is recovered rather than failing the launch.
	lockWait = 35 * time.Second
	// staleLock is the age after which a .lock file is taken to be left by a crash.
	staleLock = 30 * time.Second
	// startupGrace is how long a record without an address may be young and still count as alive.
	startupGrace = 2 * time.Minute
	// probeTimeout bounds the HTTP probe in Alive.
	probeTimeout = time.Second
)

// ErrNotOwner is returned by Handle.Update when the record is no longer this claimant's:
// another instance replaced it, or it was removed.
var ErrNotOwner = errors.New("instance record belongs to another claimant")

// PathKey is the normalised form of a deck path that identifies it: absolute and cleaned,
// and on Windows also case-folded (NTFS is case-insensitive) with `\` separators.
// It is pure, so it can be tested for either OS.
func PathKey(absPath, goos string) string {
	if goos != "windows" {
		return path.Clean(filepath.ToSlash(absPath))
	}
	s := strings.ReplaceAll(absPath, `\`, "/")
	unc := strings.HasPrefix(s, "//")
	s = path.Clean(s)
	if unc {
		s = "/" + s // path.Clean collapsed the leading // of a \\server\share path
	}
	return strings.ToLower(strings.ReplaceAll(s, "/", `\`))
}

func keyHash(absPath string) string {
	sum := sha256.Sum256([]byte(PathKey(absPath, runtime.GOOS)))
	return hex.EncodeToString(sum[:])[:16]
}

func recordPath(home, absPath string) string {
	return filepath.Join(home, "instances", keyHash(absPath)+".json")
}

func lockPath(home, absPath string) string {
	return filepath.Join(home, "instances", keyHash(absPath)+".lock")
}

// Handle is a successful claim on a deck. It carries the claimant's identity (PID and a
// random token), so Update and Release only ever touch a record that is still ours.
type Handle struct {
	home, absPath string
	pid           int
	token         string
}

// Claim registers this process as the instance for the deck at absPath.
//
// If a live record exists (alive reports true), Claim returns it and a nil handle: the
// caller should hand over to that instance. Otherwise any stale record is replaced and
// existing is nil. The new record has no port yet; call Handle.Update once Slidev is up and
// Handle.Release when done.
func Claim(home, absPath string, alive func(Record) bool) (existing *Record, h *Handle, err error) {
	if err := os.MkdirAll(filepath.Join(home, "instances"), 0o755); err != nil {
		return nil, nil, err
	}
	unlock, err := acquire(lockPath(home, absPath))
	if err != nil {
		return nil, nil, err
	}
	defer unlock()

	rp := recordPath(home, absPath)
	if cur, err := readRecord(rp); err == nil && alive(cur) {
		return &cur, nil, nil
	}

	token, err := newToken()
	if err != nil {
		return nil, nil, err
	}
	mine := Record{PID: os.Getpid(), DeckPath: absPath, Token: token, StartedAt: time.Now()}
	if err := writeRecord(rp, mine); err != nil {
		return nil, nil, err
	}
	return nil, &Handle{home: home, absPath: absPath, pid: mine.PID, token: token}, nil
}

// owned reports whether cur is this handle's record.
func (h *Handle) owned(cur Record) bool { return cur.PID == h.pid && cur.Token == h.token }

// Update records where Slidev is listening (and the work folder) once it is up. It runs under
// the lock and returns ErrNotOwner, writing nothing, when the record is no longer ours: a
// newer claimant's record must not be clobbered by a late Update from an instance that was
// judged dead.
func (h *Handle) Update(port int, baseURL, workDir string) error {
	unlock, err := acquire(lockPath(h.home, h.absPath))
	if err != nil {
		return err
	}
	defer unlock()
	rp := recordPath(h.home, h.absPath)
	cur, err := readRecord(rp)
	if err != nil || !h.owned(cur) {
		return ErrNotOwner
	}
	cur.Port, cur.BaseURL, cur.WorkDir = port, baseURL, workDir
	return writeRecord(rp, cur)
}

// Renew starts the "still starting" time of a record without an address again, for an
// instance that waited on the person (a question) before starting Slidev: while it waited, a
// second launch may have judged it dead. Like Update, it returns ErrNotOwner, writing nothing,
// when that second launch has taken the record over.
func (h *Handle) Renew() error {
	unlock, err := acquire(lockPath(h.home, h.absPath))
	if err != nil {
		return err
	}
	defer unlock()
	rp := recordPath(h.home, h.absPath)
	cur, err := readRecord(rp)
	if err != nil || !h.owned(cur) {
		return ErrNotOwner
	}
	cur.StartedAt = time.Now()
	return writeRecord(rp, cur)
}

// Release removes the record, but only while it still belongs to this claimant. It is safe to
// call more than once.
func (h *Handle) Release() {
	unlock, err := acquire(lockPath(h.home, h.absPath))
	if err != nil {
		return
	}
	defer unlock()
	rp := recordPath(h.home, h.absPath)
	if cur, err := readRecord(rp); err == nil && h.owned(cur) {
		os.Remove(rp)
	}
}

// Alive reports whether r is a running instance: its PID is running and its Slidev answers
// on the recorded address within a second. The HTTP probe is what makes a reused PID
// harmless; the PID check is what stops a foreign process on a reused port from counting.
// Only a refused or reset connection means dead: with the PID running, a probe that times out
// (a busy Vite/Slidev) leans alive, since replacing a live instance would start a second one
// on the same deck. A record without an address is an instance still starting, and counts
// while it is young.
func Alive(r Record) bool {
	if r.PID <= 0 || !pidRunning(r.PID) {
		return false
	}
	base := r.BaseURL
	if base == "" && r.Port > 0 {
		base = fmt.Sprintf("http://127.0.0.1:%d", r.Port)
	}
	if base == "" {
		return !r.StartedAt.IsZero() && time.Since(r.StartedAt) < startupGrace
	}
	client := http.Client{
		Timeout: probeTimeout,
		// An answer is an answer; do not chase redirects.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Get(strings.TrimRight(base, "/") + "/")
	if err != nil {
		var ne net.Error
		return errors.As(err, &ne) && ne.Timeout()
	}
	resp.Body.Close()
	return true
}

func readRecord(p string) (Record, error) {
	var r Record
	data, err := os.ReadFile(p)
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(data, &r)
	return r, err
}

// writeRecord replaces the record atomically, so a reader never sees half a file.
func writeRecord(p string, r Record) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), filepath.Base(p)+".*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// acquire takes the lock file with O_EXCL and writes a random token into it. The returned
// unlock removes the file only while it still holds that token, so a holder paused for longer
// than staleLock cannot delete the lock of whoever broke its own.
//
// A lock older than staleLock was left by a crash. It is broken by one waiter at a time (see
// breakLock), which renames it to a unique name and deletes that. A fresh lock is waited for
// up to lockWait.
func acquire(lock string) (func(), error) {
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(lockWait)
	for {
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, werr := f.WriteString(token)
			if err := errors.Join(werr, f.Close()); err != nil {
				os.Remove(lock)
				return nil, err
			}
			return func() { removeIfToken(lock, token) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if fi, serr := os.Stat(lock); serr == nil && time.Since(fi.ModTime()) > staleLock {
			if breakLock(lock) {
				continue
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("instance lock %s is held by another process", lock)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// breakLock removes a lock believed stale. Breakers are serialised by an O_EXCL guard file,
// and the lock is re-checked under it, so a waiter that stat'ed an old lock cannot remove the
// fresh one another breaker has since created. It returns whether it ran as the breaker.
func breakLock(lock string) bool {
	guard := lock + ".breaking"
	g, err := os.OpenFile(guard, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		// Held by another breaker for microseconds; older than staleLock means it crashed.
		if fi, serr := os.Stat(guard); serr == nil && time.Since(fi.ModTime()) > staleLock {
			os.Remove(guard)
		}
		return false
	}
	g.Close()
	defer os.Remove(guard)

	if fi, err := os.Stat(lock); err != nil || time.Since(fi.ModTime()) <= staleLock {
		return true // someone already broke it, or it is live after all
	}
	t, err := newToken()
	if err != nil {
		return true
	}
	moved := lock + ".stale-" + t
	if os.Rename(lock, moved) != nil {
		return true
	}
	if fi, err := os.Stat(moved); err == nil && time.Since(fi.ModTime()) <= staleLock {
		// Only possible if the holder released and a new holder arrived between our check and
		// the rename. Put the live lock back; if yet another lock was created meanwhile, Link
		// fails. That window is a few microseconds and needs a holder paused for staleLock.
		os.Link(moved, lock)
	}
	os.Remove(moved)
	return true
}

func removeIfToken(lock, token string) {
	if data, err := os.ReadFile(lock); err == nil && string(data) == token {
		os.Remove(lock)
	}
}

func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// List returns the readable instance records under home, live or not, so that
// the caller can tell which work folders are in use (filter with Alive).
func List(home string) []Record {
	paths, _ := filepath.Glob(filepath.Join(home, "instances", "*.json"))
	var out []Record
	for _, p := range paths {
		if r, err := readRecord(p); err == nil {
			out = append(out, r)
		}
	}
	return out
}
