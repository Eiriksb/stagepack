// Package app is what happens when a deck file is opened: it is unpacked,
// Slidev is started on it, the browser opens the presenter view, note edits
// are saved back into the file, and everything is cleaned up on stop.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"stagepack/internal/brand"
	"stagepack/internal/browser"
	"stagepack/internal/deck"
	"stagepack/internal/instance"
	"stagepack/internal/save"
	"stagepack/internal/slidev"
	"stagepack/internal/trust"
	"stagepack/internal/ui"
	"stagepack/internal/workspace"
	slidevrt "stagepack/runtime"
)

// Options say what to open and how to show it.
type Options struct {
	Home     string // the viewer's folder: node/, runtimes/, journal/, logs/, instances/
	DeckPath string
	UI       func(ui.Actions) ui.UI // default ui.NewTray
	Browser  func(url string) error // default browser.Open
	GOOS     string                 // default runtime.GOOS
	Env      func(string) string    // default os.Getenv
	// TrustCode runs a talk's code without asking (for scripts and tests). It
	// does not add the talk to the trusted list.
	TrustCode bool
}

// ErrNotTrusted is returned when the person chose not to run a talk's code;
// nothing was started.
var ErrNotTrusted = errors.New("not opened: the talk contains code and was not allowed to run it")

// maxReasonsShown is how many of the reasons a talk contains code are listed in the question.
const maxReasonsShown = 8

// server is the part of *slidev.Server the app uses; tests fake it.
type server interface {
	Port() int
	BaseURL() string
	Stop() error
	Done() <-chan error
}

// readHeadmatter is slidev.ReadHeadmatter; tests replace it.
var readHeadmatter = slidev.ReadHeadmatter

// startSlidev is slidev.Start; tests replace it.
var startSlidev = func(ctx context.Context, c slidev.Config, port int) (server, error) {
	s, err := slidev.Start(ctx, c, port)
	if err != nil {
		return nil, err
	}
	return s, nil
}

const (
	// staleAge: a work folder younger than this may belong to an instance still starting.
	staleAge        = 10 * time.Minute
	throwawayDetail = "This is a temporary copy; notes you edit won't be kept. Use Save a copy…"
	failedDetail    = "Couldn't save into the file (it may be read-only, or the USB stick was removed). Your notes are kept; use Save a copy…"
)

type session struct {
	o       Options
	home    string
	rtDir   string
	ui      ui.UI
	journal save.Journal
	work    workspace.Work
	entry   string
	title   string
	logPath string
	ctx     context.Context // cancelled by stop: aborts a Slidev that is still starting
	cancel  context.CancelFunc
	started chan struct{} // closed when start has finished
	once    sync.Once     // stop
	opened  func()        // closes the "Opening" box; safe to call more than once

	opMu sync.Mutex // serialises Save a copy and stop

	mu        sync.Mutex // guards what follows; never held while calling the watcher
	begun     bool       // start ran, or stop ruled it out
	stopping  bool
	srv       server
	watcher   *save.Watcher
	watchStop context.CancelFunc
	watchDone chan struct{}
	handle    *instance.Handle
	deckPath  string // where notes are saved: the opened file, or the last copy
	key       string // its journal key
	throwaway bool
	err       error
}

// Open opens the deck and returns when it has been stopped. Errors are shown
// to the user through the UI before they are returned.
func Open(ctx context.Context, o Options) error {
	if o.UI == nil {
		o.UI = ui.NewTray
	}
	if o.Browser == nil {
		o.Browser = browser.Open
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.Env == nil {
		o.Env = os.Getenv
	}
	home, err := filepath.Abs(o.Home)
	if err != nil {
		return err
	}
	deckPath, err := filepath.Abs(o.DeckPath)
	if err != nil {
		return err
	}
	s := &session{
		o: o, home: home, deckPath: deckPath, title: filepath.Base(deckPath),
		rtDir:   filepath.Join(home, "runtimes", strconv.Itoa(slidevrt.Major())),
		journal: save.Journal{Dir: filepath.Join(home, "journal")},
		started: make(chan struct{}),
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	defer s.cancel()
	s.ui = o.UI(ui.Actions{
		OpenPresenter: func() { s.openURL("/presenter/") },
		OpenAudience:  func() { s.openURL("/") },
		SaveCopy:      s.saveCopy,
		Stop:          s.stop,
	})
	// Unpacking and starting Slidev take a while: say so until the browser is open.
	name := strings.TrimSuffix(filepath.Base(deckPath), filepath.Ext(deckPath))
	s.opened = sync.OnceFunc(s.ui.Opening(name))
	defer func() { s.opened() }() // any return before the UI runs

	// 1. Work folders left by a crash.
	if err := workspace.CleanStale(s.rtDir, s.inUse()); err != nil {
		log.Printf("clean old work folders: %v", err)
	}
	// 2. One instance per deck: a second launch shows the running one.
	existing, h, err := instance.Claim(home, deckPath, instance.Alive)
	if err != nil {
		s.opened()
		s.ui.Error(brand.Name, fmt.Sprintf("Couldn't open %s:\n%v", filepath.Base(deckPath), err))
		return err
	}
	if existing != nil {
		if existing.BaseURL == "" {
			// Still starting; it opens the browser itself.
			s.opened()
			s.ui.Notify(brand.Name, name+" is already opening…")
			return nil
		}
		err := o.Browser(existing.BaseURL + "/presenter/")
		s.opened()
		return err
	}
	s.handle = h
	// 3. Unpack.
	work, err := workspace.Extract(deckPath, s.rtDir)
	if err != nil {
		h.Release()
		s.opened()
		s.ui.Error(openError(deckPath, err))
		return err
	}
	m := work.Manifest
	s.work, s.entry = work, m.Entry // Extract checked it: a plain .md name at the top
	if m.Title != "" {
		s.title = m.Title
	}
	// 3a. The theme and addons Slidev will load, as its own parser reads them.
	if hm, err := readHeadmatter(s.ctx, s.nodePath(), s.rtDir, filepath.Join(work.Dir, m.Entry)); err != nil {
		log.Printf("check the headmatter of %s: %v", deckPath, err)
		work.AddCode("the slides' headmatter (it couldn't be checked)")
	} else {
		work.AddCode(deck.HeadmatterReasons(hm.Theme, hm.Addons)...)
	}
	s.work = work
	// 3b. Code the talk brought along runs as this user once Slidev starts: ask
	// first, once for each version of the talk.
	if work.HasCode() {
		if !s.allowCode(name) {
			workspace.Remove(work)
			h.Release()
			return ErrNotTrusted
		}
		// The question may have been up for longer than a claim counts as starting, and
		// another launch of the talk may have taken it over meanwhile: that one shows it.
		if err := h.Renew(); errors.Is(err, instance.ErrNotOwner) {
			workspace.Remove(work)
			s.opened()
			s.ui.Notify(brand.Name, name+" was opened again meanwhile, and is shown there.")
			return nil
		} else if err != nil {
			log.Printf("renew the claim on %s: %v", deckPath, err)
		}
	}
	s.key = save.JournalKey(m.ID, deckPath)
	s.logPath = filepath.Join(home, "logs", s.key+".log")
	// 4. Edits a crash left in the journal. An entry that can't be applied is
	// moved aside before the watcher could overwrite it; if even that fails,
	// the deck is not opened.
	if _, kept, err := save.Recover(s.journal, s.key, m, filepath.Join(work.Dir, s.entry)); err != nil {
		log.Printf("recover notes: %v", err)
		if kept, err2 := save.SetAside(s.journal, s.key); err2 != nil {
			workspace.Remove(work)
			h.Release()
			entry := filepath.Join(s.journal.Dir, s.key+".json")
			s.opened()
			s.ui.Error(s.title, fmt.Sprintf("Notes from an earlier session could not be restored (%v), and could not be set aside (%v).\n\nThey are in %s. Copy that file somewhere safe before opening this talk again.", err, err2, entry))
			return fmt.Errorf("recover notes: %w; set aside: %w", err, err2)
		} else if kept != "" {
			s.ui.Notify(s.title, fmt.Sprintf("Notes from an earlier session could not be restored (%v). They are kept in %s", err, kept))
		}
	} else if kept != "" {
		s.ui.Notify(s.title, "Notes from an earlier session were made on an older version of this file, so they were not applied. They are kept in "+kept)
	}
	// 5. Show that something is happening; the rest runs once the UI is up.
	s.ui.SetStatus(s.title, save.Pending, "Starting "+s.title+"…")
	defer context.AfterFunc(ctx, s.stop)()
	s.ui.Run(func() { go s.start() })
	s.stop() // in case the UI ended by itself
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// allowCode reports whether the talk's code may run: it is in the trusted list
// in this version, or the person says yes now (which adds it to the list).
// name is the talk's file name, for the "Opening" box shown again after a yes.
func (s *session) allowCode(name string) bool {
	if s.o.TrustCode {
		return true
	}
	w := s.work
	store := trust.Store{Path: filepath.Join(s.home, trust.FileName)}
	if store.Trusted(w.Manifest.ID, w.Hash) {
		return true
	}
	s.opened()
	if !s.ui.Confirm(brand.Name, codeQuestion(filepath.Base(s.deckPath), s.title, w.Code)) {
		log.Printf("%s not opened: its code was not allowed to run", s.deckPath)
		return false
	}
	if err := store.Remember(w.Manifest.ID, w.Hash, s.title); err != nil {
		log.Printf("remember that %s may run its code: %v", s.deckPath, err)
	}
	s.opened = sync.OnceFunc(s.ui.Opening(name)) // starting still takes a while
	return true
}

// codeQuestion is the text of the question asked before a talk's code runs.
// The title and reasons come from the file, so they are shown on one line
// each and cut short.
func codeQuestion(file, title string, reasons []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%q (%s) contains code. It runs on this computer, with access to your files, as soon as the talk is shown:\n\n",
		oneLine(title, 80), oneLine(file, 80))
	for i, r := range reasons {
		if i == maxReasonsShown {
			fmt.Fprintf(&b, "  • and %d more\n", len(reasons)-i)
			break
		}
		b.WriteString("  • " + oneLine(r, 100) + "\n")
	}
	b.WriteString("\nOnly choose Yes if you trust the person who sent you this file.\n\n" +
		"Yes: show the talk, and don't ask again for this version of it.\n" +
		"No: close it without running anything.")
	return b.String()
}

// oneLine is s with control characters as spaces, cut to max characters.
func oneLine(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, s)
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max-1]) + "…"
	}
	return s
}

// openError is the dialog title and text for a deck that can't be unpacked.
func openError(deckPath string, err error) (title, msg string) {
	var tn *deck.TooNewError
	if errors.As(err, &tn) {
		msg = tn.Error()
		r, n := utf8.DecodeRuneInString(msg)
		msg = string(unicode.ToUpper(r)) + msg[n:]
		if tn.Older {
			return "This talk needs repacking", msg
		}
		return "A newer " + brand.Name + " viewer is needed", msg
	}
	return brand.Name, fmt.Sprintf("Couldn't open %s:\n%v", filepath.Base(deckPath), err)
}

// inUse tells CleanStale which work folders to keep: young ones (an instance may
// be starting in it) and those of live instances.
func (s *session) inUse() func(dir string) bool {
	var live []instance.Record
	loaded := false
	return func(dir string) bool {
		if fi, err := os.Stat(dir); err == nil && time.Since(fi.ModTime()) < staleAge {
			return true
		}
		if !loaded {
			loaded = true
			for _, r := range instance.List(s.home) {
				if r.WorkDir != "" && instance.Alive(r) {
					live = append(live, r)
				}
			}
		}
		for _, r := range live {
			if instance.PathKey(r.WorkDir, s.o.GOOS) == instance.PathKey(dir, s.o.GOOS) {
				return true
			}
		}
		return false
	}
}

func (s *session) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		s.err = err
	}
}

// start runs steps 6–9 on its own goroutine, and stops if they fail.
func (s *session) start() {
	s.mu.Lock()
	if s.begun {
		s.mu.Unlock()
		return
	}
	s.begun = true
	s.mu.Unlock()
	err := s.startup()
	close(s.started)
	if err != nil {
		s.setErr(err)
		s.stop()
	}
}

// nodePath is the bundled node program.
func (s *session) nodePath() string {
	if s.o.GOOS == "windows" {
		return filepath.Join(s.home, "node", "node.exe")
	}
	return filepath.Join(s.home, "node", "bin", "node")
}

func (s *session) startup() error {
	cfg := slidev.Config{
		Node: s.nodePath(), CLI: filepath.Join(s.rtDir, "node_modules", "@slidev", "cli", "bin", "slidev.mjs"),
		WorkDir: s.work.Dir, Entry: s.entry, LogPath: s.logPath,
	}
	port, err := slidev.FreePort()
	if err == nil {
		err = os.MkdirAll(filepath.Dir(s.logPath), 0o755)
	}
	var srv server
	if err == nil {
		srv, err = startSlidev(s.ctx, cfg, port) // 6.
	}
	if err != nil {
		s.opened()
		if s.ctx.Err() != nil {
			return nil // stopped while starting
		}
		s.ui.Error(s.title, "The talk couldn't be started:\n\n"+err.Error())
		return err
	}
	s.mu.Lock()
	s.srv = srv
	s.mu.Unlock()
	go s.watchServer(srv)
	if err := s.handle.Update(srv.Port(), srv.BaseURL(), s.work.Dir); err != nil { // 7.
		log.Printf("record the running instance: %v", err)
	}
	s.openURL("/presenter/") // 8.
	s.opened()

	// 9. Save note edits.
	m := s.work.Manifest
	w := save.NewWatcher(save.WatcherConfig{
		SlidesPath: filepath.Join(s.work.Dir, s.entry), DeckPath: s.deckPath, EntryName: s.entry,
		DeckID: m.ID, Key: s.key, Journal: s.journal, Base: m.Updated, OnStatus: s.onStatus,
	})
	wctx, wstop := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.mu.Lock()
	s.watcher, s.watchStop, s.watchDone = w, wstop, done
	s.throwaway = instance.IsThrowawayCopy(s.deckPath, s.o.GOOS, s.o.Env)
	s.showStatus(save.Saved)
	s.mu.Unlock()
	go func() { w.Run(wctx); close(done) }()
	return nil
}

// onStatus runs under the watcher's lock: it only forwards to the UI.
func (s *session) onStatus(st save.Status, err error) {
	if err != nil {
		log.Printf("save notes: %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.showStatus(st)
}

// showStatus shows the watcher's status st, as a throwaway copy if it is one.
// s.mu is held.
func (s *session) showStatus(st save.Status) {
	detail := ""
	switch {
	case st == save.Failed:
		detail = failedDetail
	case s.throwaway:
		st, detail = save.Throwaway, throwawayDetail
	}
	s.ui.SetStatus(s.title, st, detail)
}

// watchServer stops everything if Slidev exits by itself (12.).
func (s *session) watchServer(srv server) {
	err := <-srv.Done()
	s.mu.Lock()
	stopping := s.stopping
	s.mu.Unlock()
	if stopping {
		return
	}
	msg := "Slidev stopped unexpectedly"
	if err != nil {
		msg += ": " + err.Error()
	}
	s.opened()
	s.ui.Error(s.title, msg+"\n\n"+slidev.LogTail(s.logPath))
	s.setErr(errors.New(msg))
	s.stop()
}

func (s *session) openURL(path string) {
	s.mu.Lock()
	srv := s.srv
	s.mu.Unlock()
	if srv == nil {
		return
	}
	u := srv.BaseURL() + path
	if err := s.o.Browser(u); err != nil {
		s.ui.Notify(s.title, fmt.Sprintf("Couldn't open the browser (%v). Open %s yourself.", err, u))
	}
}

// stop (11.): Slidev first, so it is not writing the slides file while it is
// saved; then the last save, the work folder, the instance record, the UI.
func (s *session) stop() {
	s.once.Do(func() {
		s.opened()
		s.opMu.Lock()
		defer s.opMu.Unlock()
		s.mu.Lock()
		s.stopping = true
		begun := s.begun
		s.begun = true
		s.mu.Unlock()
		s.cancel()
		if begun {
			<-s.started
		}
		s.mu.Lock()
		srv, w, wstop, wdone := s.srv, s.watcher, s.watchStop, s.watchDone
		s.mu.Unlock()
		if srv != nil {
			if err := srv.Stop(); err != nil {
				log.Printf("stop Slidev: %v", err)
			}
		}
		keepWork := false
		if w != nil {
			wstop()
			<-wdone
			// Slidev may have been killed in the middle of writing the slides file.
			if err := w.FlushSettled(); err != nil {
				keepWork = s.flushFailed(err)
			}
		}
		if s.work.Dir != "" && !keepWork {
			if err := workspace.Remove(s.work); err != nil {
				log.Printf("remove the work folder: %v", err)
			}
		}
		s.mu.Lock()
		h := s.handle
		s.mu.Unlock()
		if h != nil {
			h.Release()
		}
		s.ui.Quit()
	})
}

// flushFailed tells the user where the notes are. If the journal doesn't have
// them either, the work folder is kept and named instead.
func (s *session) flushFailed(err error) (keepWork bool) {
	s.mu.Lock()
	key, target := s.key, s.deckPath
	s.mu.Unlock()
	where := filepath.Join(s.journal.Dir, key+".json")
	if _, ok, jerr := s.journal.Get(key); jerr != nil || !ok {
		where, keepWork = filepath.Join(s.work.Dir, s.entry), true
	}
	s.setErr(err)
	s.ui.Error(s.title, fmt.Sprintf("Your notes could not be written into %s:\n%v\n\nThey are kept in %s.", target, err, where))
	return keepWork
}

// saveCopy (10.) copies the deck to a place the user picks and saves notes there from now on.
func (s *session) saveCopy() {
	s.mu.Lock()
	w, srv, stopping := s.watcher, s.srv, s.stopping
	s.mu.Unlock()
	if w == nil || stopping {
		return
	}
	dst, ok := s.ui.AskSavePath(s.suggestCopyPath())
	if !ok || dst == "" {
		return
	}
	if !strings.EqualFold(filepath.Ext(dst), brand.Ext) {
		dst += brand.Ext
	}
	dst, err := filepath.Abs(dst)
	if err != nil {
		s.ui.Error(s.title, err.Error())
		return
	}

	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	cur, stopping := s.deckPath, s.stopping
	s.mu.Unlock()
	if stopping || instance.PathKey(dst, s.o.GOOS) == instance.PathKey(cur, s.o.GOOS) {
		return
	}
	if err := w.Flush(); err != nil {
		log.Printf("save a copy: save into %s first: %v", cur, err) // the copy still gets the notes below
	}
	// Claim before copying, so a file open in another window is never overwritten.
	existing, h2, err := instance.Claim(s.home, dst, instance.Alive)
	if err == nil && existing != nil {
		err = errors.New("it is open in another " + brand.Name + " window; stop it there first, or choose another name")
	}
	if err == nil {
		if err = copyFile(cur, dst); err != nil {
			// The deck file is unreadable (a USB stick pulled, say): build the copy from the work folder.
			log.Printf("save a copy: copy %s: %v; rebuilding from the work folder", cur, err)
			err = workspace.Rebuild(s.work, dst, time.Now().UTC().Round(0))
		}
		if err == nil {
			if uerr := h2.Update(srv.Port(), srv.BaseURL(), s.work.Dir); uerr != nil {
				log.Printf("record the running instance: %v", uerr)
			}
			err = w.SetTarget(dst, save.JournalKey(s.work.Manifest.ID, dst))
		}
		if err != nil {
			h2.Release()
		}
	}
	if err != nil {
		s.ui.Error(s.title, fmt.Sprintf("Couldn't save a copy as %s:\n%v", dst, err))
		return
	}
	ferr := w.Flush()
	s.mu.Lock()
	old := s.handle
	s.handle, s.deckPath, s.key = h2, dst, save.JournalKey(s.work.Manifest.ID, dst)
	s.throwaway = instance.IsThrowawayCopy(dst, s.o.GOOS, s.o.Env)
	s.showStatus(w.Status())
	s.mu.Unlock()
	old.Release()
	if ferr == nil {
		s.ui.Notify(s.title, "Saved a copy as "+dst+". Notes you edit now go into it.")
	}
}

// suggestCopyPath is "<title>" + brand.Ext in Documents, else Downloads, else the current folder.
func (s *session) suggestCopyPath() string {
	name := strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '-'
		}
		return r
	}, s.title)
	name = strings.Trim(name, " .")
	if name == "" {
		name = "talk"
	}
	name += brand.Ext
	if home, err := os.UserHomeDir(); err == nil {
		for _, d := range []string{"Documents", "Downloads"} {
			if fi, err := os.Stat(filepath.Join(home, d)); err == nil && fi.IsDir() {
				return filepath.Join(home, d, name)
			}
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return filepath.Join(wd, name)
	}
	return name
}

// copyFile streams src to dst+".tmp" and renames it over dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
