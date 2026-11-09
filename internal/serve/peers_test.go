package serve

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"stagepack/internal/brand"
	"stagepack/internal/instance"
)

// liveViewer is a viewer with a running Slidev, recorded as holding deckPath in home.
func liveViewer(t *testing.T, home, deckPath string) (string, *instance.Handle) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(srv.Close)
	return srv.URL, takeOver(t, home, deckPath, srv.URL)
}

// takeOver replaces whatever record home has for deckPath with one at base.
func takeOver(t *testing.T, home, deckPath, base string) *instance.Handle {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	_, other, err := instance.Claim(home, deckPath, func(instance.Record) bool { return false })
	if err != nil || other == nil {
		t.Fatalf("take over: %v", err)
	}
	if err := other.Update(port, base, "/elsewhere/work"); err != nil {
		t.Fatal(err)
	}
	return other
}

// safeBuffer is a log destination for a test whose logging happens on Run's goroutine.
type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func captureLog(t *testing.T) *safeBuffer {
	t.Helper()
	b := &safeBuffer{}
	log.SetOutput(b)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return b
}

func checkOnlyRecord(t *testing.T, home, base string) {
	t.Helper()
	if recs := instance.List(home); len(recs) != 1 || recs[0].BaseURL != base {
		t.Errorf("records in %s = %+v, want only the one at %s", home, recs, base)
	}
}

// The viewer and the editor have different homes: a talk open in the one is open for the other.
func TestServeBusyAcrossHomes(t *testing.T) {
	home, peer := t.TempDir(), t.TempDir()
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, nil)
	base, _ := liveViewer(t, peer, deckPath)

	h := serveDeck(t, Options{Home: home, PeerHomes: []string{peer}, DeckPath: deckPath})
	if got := str(t, h.expect("busy"), "baseUrl"); got != base {
		t.Errorf("busy baseUrl = %s, want %s", got, base)
	}
	if code := h.wait(); code != 4 {
		t.Errorf("exit %d, want 4", code)
	}
	if n := len(h.fake.configs()); n != 0 {
		t.Errorf("Slidev was started %d times", n)
	}
	if left := workFolders(t, home); len(left) > 0 {
		t.Errorf("the deck was unpacked: %q", left)
	}
	if recs := instance.List(home); len(recs) != 0 {
		t.Errorf("the claim in the own home was kept: %+v", recs)
	}
	checkOnlyRecord(t, peer, base)
}

// Busy in a later peer home gives up the claims already made in the earlier ones.
func TestServeBusyInLaterPeerReleasesEarlierClaims(t *testing.T) {
	home, first, second := t.TempDir(), t.TempDir(), t.TempDir()
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, nil)
	base, _ := liveViewer(t, second, deckPath)

	h := serveDeck(t, Options{Home: home, PeerHomes: []string{first, second}, DeckPath: deckPath})
	h.expect("busy")
	if code := h.wait(); code != 4 {
		t.Errorf("exit %d, want 4", code)
	}
	for _, d := range []string{home, first} {
		if recs := instance.List(d); len(recs) != 0 {
			t.Errorf("records in %s: %+v, want none", d, recs)
		}
	}
	checkOnlyRecord(t, second, base)
}

// A plain Stagepack that looks in its own home finds this one's record, ready for a hand-over.
func TestServeClaimsPeerHome(t *testing.T) {
	home, peer := t.TempDir(), t.TempDir()
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, nil)
	h := serveDeck(t, Options{Home: home, PeerHomes: []string{peer}, DeckPath: deckPath})
	workDir := checkUnpacked(t, h.expect("unpacked"), home, deckPath, true)
	ready := h.expect("ready")

	recs := instance.List(peer)
	if len(recs) != 1 {
		t.Fatalf("records in the peer home = %+v, want one", recs)
	}
	r := recs[0]
	if r.PID != os.Getpid() || float64(r.Port) != ready["port"] || r.BaseURL != str(t, ready, "baseUrl") ||
		r.WorkDir != workDir || r.DeckPath != deckPath || r.Token == "" || r.StartedAt.IsZero() {
		t.Errorf("the peer record = %+v; ready = %v, workDir %s, deck %s", r, ready, workDir, deckPath)
	}

	h.send(`{"cmd":"stop"}`)
	h.expect("stopped")
	if code := h.wait(); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
	if recs := instance.List(peer); len(recs) != 0 {
		t.Errorf("records in the peer home after stop = %+v, want none", recs)
	}
	checkCleanedUp(t, home, workDir)
}

// A claim can be lost in the own home while Slidev starts. This one then gives way.
func TestServeTakenOverWhileStarting(t *testing.T) {
	for name, inPeer := range map[string]bool{"own home": false, "peer home": true} {
		t.Run(name, func(t *testing.T) {
			home, peer := t.TempDir(), t.TempDir()
			deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, nil)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
			defer srv.Close()
			lost := home
			if inPeer {
				lost = peer
			}
			fake := &fakeStart{during: func() { takeOver(t, lost, deckPath, srv.URL) }}
			h := serveWith(t, Options{Home: home, PeerHomes: []string{peer}, DeckPath: deckPath}, fake)
			workDir := checkUnpacked(t, h.expect("unpacked"), home, deckPath, true)
			if got := str(t, h.expect("busy"), "baseUrl"); got != srv.URL {
				t.Errorf("busy baseUrl = %s, want %s", got, srv.URL)
			}
			if code := h.wait(); code != 4 {
				t.Errorf("exit %d, want 4", code)
			}
			select {
			case <-fake.server().done:
			default:
				t.Error("Slidev was not stopped")
			}
			if _, err := os.Stat(workDir); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the work folder is still there (err %v)", err)
			}
			checkOnlyRecord(t, lost, srv.URL)
			for _, d := range []string{home, peer} {
				if d != lost {
					if recs := instance.List(d); len(recs) != 0 {
						t.Errorf("this launch's record stayed in %s: %+v", d, recs)
					}
				}
			}
		})
	}
}

// The question can be up for long enough to lose the claim in a peer home too.
func TestServeTakenOverInPeerHomeWhileAsking(t *testing.T) {
	home, peer := t.TempDir(), t.TempDir()
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, codeFiles)
	h := serveDeck(t, Options{Home: home, PeerHomes: []string{peer}, DeckPath: deckPath})
	checkUnpacked(t, h.expect("unpacked"), home, deckPath, false)
	h.expect("confirm")
	base, _ := liveViewer(t, peer, deckPath)

	h.send(`{"answer":"yes"}`)
	if got := str(t, h.expect("busy"), "baseUrl"); got != base {
		t.Errorf("busy baseUrl = %s, want %s", got, base)
	}
	if code := h.wait(); code != 4 {
		t.Errorf("exit %d, want 4", code)
	}
	if n := len(h.fake.configs()); n != 0 {
		t.Errorf("Slidev was started %d times", n)
	}
	checkOnlyRecord(t, peer, base)
	if recs := instance.List(home); len(recs) != 0 {
		t.Errorf("the own home's record stayed: %+v", recs)
	}
}

// A peer home is only a courtesy to the other program: it can never stop a talk from opening.
func TestServeSkipsPeerHomeItCannotWrite(t *testing.T) {
	logged := captureLog(t)
	home, peer := t.TempDir(), t.TempDir()
	notAFolder := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notAFolder, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, nil)
	h := serveDeck(t, Options{Home: home, PeerHomes: []string{notAFolder, peer}, DeckPath: deckPath})
	h.expect("unpacked")
	h.expect("ready")
	if recs := instance.List(peer); len(recs) != 1 {
		t.Errorf("the other peer home's records = %+v, want this launch's", recs)
	}
	if got := logged.String(); !strings.Contains(got, notAFolder) {
		t.Errorf("log = %q, want a line naming %s", got, notAFolder)
	}
	h.send(`{"cmd":"stop"}`)
	h.expect("stopped")
	if code := h.wait(); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
}

// A record that simply vanished from a peer home is not a takeover.
func TestServePeerRecordGoneIsNotFatal(t *testing.T) {
	logged := captureLog(t)
	home, peer := t.TempDir(), t.TempDir()
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, nil)
	fake := &fakeStart{during: func() {
		for _, r := range []string{"instances"} {
			if err := os.RemoveAll(filepath.Join(peer, r)); err != nil {
				t.Error(err)
			}
		}
	}}
	h := serveWith(t, Options{Home: home, PeerHomes: []string{peer}, DeckPath: deckPath}, fake)
	h.expect("unpacked")
	h.expect("ready")
	if got := logged.String(); !strings.Contains(got, peer) {
		t.Errorf("log = %q, want a line naming %s", got, peer)
	}
	h.send(`{"cmd":"stop"}`)
	h.expect("stopped")
	if code := h.wait(); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
}

// The own home named as a peer, directly or through a link, is one home.
func TestServeOwnHomeAsPeerIsOneHome(t *testing.T) {
	home := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	peers := []string{home, home}
	if err := os.Symlink(home, link); err == nil {
		peers = append(peers, link)
	}
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, nil)
	h := serveDeck(t, Options{Home: home, PeerHomes: peers, DeckPath: deckPath})
	h.expect("unpacked")
	h.expect("ready")
	if recs := instance.List(home); len(recs) != 1 {
		t.Errorf("records = %+v, want one", recs)
	}
	h.send(`{"cmd":"stop"}`)
	h.expect("stopped")
	if code := h.wait(); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
}

// A link to a deck is the deck: one claim, and a save replaces the file, not the link.
func TestServeSymlinkedDeck(t *testing.T) {
	home := t.TempDir()
	target := packFixture(t, t.TempDir(), "real"+brand.Ext, nil)
	link := filepath.Join(t.TempDir(), "link"+brand.Ext)
	if err := os.Symlink(target, link); err != nil {
		t.Skip("no symlinks:", err)
	}
	h := serveDeck(t, Options{Home: home, DeckPath: link})
	workDir := checkUnpacked(t, h.expect("unpacked"), home, target, true)
	h.expect("ready")

	recs := instance.List(home)
	if len(recs) != 1 || recs[0].DeckPath != target {
		t.Errorf("records = %+v, want one for %s", recs, target)
	}
	// The real path finds the claim made through the link.
	if existing, other, err := instance.Claim(home, target, func(instance.Record) bool { return true }); err != nil || existing == nil {
		if other != nil {
			other.Release()
		}
		t.Errorf("a claim on the real path got %+v, %v; want the link's claim", existing, err)
	}

	slides := filepath.Join(workDir, "slides.md")
	data, err := os.ReadFile(slides)
	if err != nil {
		t.Fatal(err)
	}
	edited := string(data) + "\nEdited through a link.\n"
	if err := os.WriteFile(slides, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	h.send(`{"cmd":"save"}`)
	h.expect("saved")
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced (mode %v, err %v)", fi.Mode(), err)
	}
	if got := entryText(t, target, "slides.md"); got != edited {
		t.Errorf("the real deck's slides.md = %q, want the edit", got)
	}
	h.send(`{"cmd":"stop"}`)
	h.expect("stopped")
	if code := h.wait(); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
}

// Slidev rewrites slides.md in place; a save in that moment is refused and serving goes on.
func TestServeSaveWhileSlidesAreEmpty(t *testing.T) {
	home := t.TempDir()
	deckPath := packFixture(t, t.TempDir(), "talk"+brand.Ext, nil)
	h := serveDeck(t, Options{Home: home, DeckPath: deckPath})
	workDir := checkUnpacked(t, h.expect("unpacked"), home, deckPath, true)
	h.expect("ready")
	before, err := os.ReadFile(deckPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "slides.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h.send(`{"cmd":"save"}`)
	ev := h.expect("save-failed")
	if msg := str(t, ev, "message"); msg != "the slides file is being written; save again in a moment" {
		t.Errorf("save-failed message = %q", msg)
	}
	if names := strs(t, ev, "names"); len(names) != 0 {
		t.Errorf("save-failed names = %q, want none", names)
	}
	if after, err := os.ReadFile(deckPath); err != nil || !bytes.Equal(after, before) {
		t.Errorf("the deck changed (err %v)", err)
	}
	h.send(`{"cmd":"stop"}`)
	h.expect("stopped")
	h.wait()
}
