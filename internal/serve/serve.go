// Package serve is `stagepack serve --json`, a long-lived sidecar for an editor. It unpacks a
// deck, asks the trust question over stdin and stdout, runs Slidev on the work folder with the
// editor's addons loaded, saves the whole work folder back into the deck on command, and cleans
// up when it stops. Everything happens in the one process: its instance record (internal/instance)
// is what keeps other Stagepack launches, and their clean-up of old work folders, away from the
// work folder while it runs.
//
// Stdout carries only events, one JSON object per line; stdin carries commands, one JSON object
// per line. Their shapes and the exit codes are a contract with the editor:
//
//	{"event":"unpacked","id":"…","title":"…","entry":"slides.md","workDir":"/abs","code":["…"],"trusted":false}
//	{"event":"confirm","question":"…"}                 only for code that isn't trusted
//	{"event":"ready","port":41234,"baseUrl":"http://127.0.0.1:41234","workDir":"/abs"}
//	{"event":"saved","updated":"2026-11-09T12:00:00Z"}  after each save
//	{"event":"conflict","message":"…"}                  the file changed on disk: not saved
//	{"event":"save-failed","message":"…","names":["public/a:b.png"]}
//	{"event":"busy","baseUrl":"http://…"}               open in another Stagepack; exit 4
//	{"event":"error","message":"…","log":"<last lines of Slidev's log>"}
//	{"event":"stopped"}                                 the last event when the exit is 0 or 1
//
//	{"answer":"yes"} {"answer":"no"} {"cmd":"save"} {"cmd":"stop"}
//
// The end of stdin stops it, as {"cmd":"stop"} and the context (SIGINT, SIGTERM) do. A line
// that isn't one of the commands gets an error event, and serving goes on; an answer while no
// question is pending is ignored. Exit codes: 0 stopped, 1 error (Slidev died or couldn't
// start), 2 usage (cmd/stagepack), 3 not trusted (no event; the work folder is removed), 4 busy.
package serve

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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

	"stagepack/internal/app"
	"stagepack/internal/instance"
	"stagepack/internal/save"
	"stagepack/internal/slidev"
	"stagepack/internal/trust"
	"stagepack/internal/workspace"
	slidevrt "stagepack/runtime"
)

// Exit codes of Run.
const (
	ExitStopped    = 0
	ExitError      = 1
	ExitUsage      = 2 // for cmd/stagepack; Run never returns it
	ExitNotTrusted = 3
	ExitBusy       = 4
)

// Options say what to serve.
type Options struct {
	Home     string // the viewer's folder: node/, runtimes/, logs/, instances/, trusted.json
	DeckPath string
	// TrustCode runs the talk's code without asking, and without adding it to the trusted
	// list, also when it is saved.
	TrustCode bool
	// Addons are folders outside the talk that Slidev loads on top of the talk's own
	// (slidev.AddonLaunch): the editor's, which never count as the talk's code and are never
	// written into the work folder or the deck.
	Addons []string
}

// Server is the part of a running Slidev (*slidev.Server) that Run uses.
type Server interface {
	Port() int
	BaseURL() string
	Done() <-chan error
	Stop() error
}

// Deps are what Run takes from outside; tests replace them. A nil field is the real thing.
type Deps struct {
	Start func(ctx context.Context, c slidev.Config, port int) (Server, error) // slidev.Start
	Now   func() time.Time                                                     // time.Now
}

// readHeadmatter is slidev.ReadHeadmatter; tests replace it.
var readHeadmatter = slidev.ReadHeadmatter

func startSlidev(ctx context.Context, c slidev.Config, port int) (Server, error) {
	s, err := slidev.Start(ctx, c, port)
	if err != nil {
		return nil, err // not a nil *slidev.Server in a non-nil Server
	}
	return s, nil
}

const (
	conflictMessage = "This file was changed by another program since it was opened, so it was not overwritten."
	notOpenMessage  = "The talk is not open yet, so there is nothing to save."
	// maxStarts is how many times Slidev is started when it loses the race for its port: the
	// port FreePort found is free a moment before Slidev binds it, and with strictPort Slidev
	// exits when another program took it meanwhile.
	maxStarts = 3
	// exitedEarly is how slidev.Start says Slidev exited before it answered, as it does on a
	// lost port race.
	exitedEarly = "slidev exited before it was ready"
)

// Run serves the deck in o until it is stopped, reading commands from in and writing events to
// out, and returns the exit code. See the package comment.
func Run(ctx context.Context, o Options, in io.Reader, out io.Writer, d Deps) int {
	if d.Start == nil {
		d.Start = startSlidev
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	done := make(chan struct{})
	defer close(done)
	s := &session{o: o, d: d, out: &emitter{w: out}, cmds: readCommands(in, done)}
	return s.run(ctx)
}

// session is one Run. Only Run's goroutine uses it.
type session struct {
	o    Options
	d    Deps
	out  *emitter
	cmds <-chan command // closed at the end of stdin

	home, deckPath, rtDir string
	handle                *instance.Handle // the claim on the deck, once made
	work                  workspace.Work   // Dir is "" until it is unpacked, and after it is removed
	srv                   Server
	title, logPath        string
	check                 app.CodeCheck
	// keepTrust: the talk was trusted when it was opened (no code, or this version in the
	// trusted list), or the person said yes to it. A save then adds the saved version to the
	// list, so the next open doesn't ask about the talk's own edits.
	keepTrust bool
}

func (s *session) run(ctx context.Context) int {
	var err error
	if s.home, err = filepath.Abs(s.o.Home); err == nil {
		s.deckPath, err = filepath.Abs(s.o.DeckPath)
	}
	if err != nil {
		return s.fail(err.Error(), "")
	}
	file := filepath.Base(s.deckPath)
	s.rtDir = filepath.Join(s.home, "runtimes", strconv.Itoa(slidevrt.Major()))

	// Work folders left by a crash. Ours is kept from the next clean-up by the claim.
	if err := workspace.CleanStale(s.rtDir, instance.InUse(s.home, s.d.Now())); err != nil {
		log.Printf("clean old work folders: %v", err)
	}
	// One instance per deck: the viewer, or another editor, may have it open.
	existing, h, err := instance.Claim(s.home, s.deckPath, instance.Alive)
	if err != nil {
		return s.fail(fmt.Sprintf("Couldn't open %s: %v", file, err), "")
	}
	if existing != nil {
		s.out.emit(busyEvent{Event: "busy", BaseURL: existing.BaseURL})
		return ExitBusy
	}
	s.handle = h

	work, err := workspace.Extract(s.deckPath, s.rtDir)
	if err != nil {
		return s.fail(fmt.Sprintf("Couldn't open %s: %v", file, err), "")
	}
	s.work = work
	m := work.Manifest
	s.title = m.Title
	if s.title == "" {
		s.title = file
	}

	// The talk's code runs as this user once Slidev starts: ask first, once per version.
	s.check = app.CodeCheck{Home: s.home, Node: nodePath(s.home), RuntimeDir: s.rtDir, TrustCode: s.o.TrustCode,
		ReadHeadmatter: readHeadmatter}
	reasons, trusted := s.check.Assess(ctx, &s.work, s.deckPath)
	s.keepTrust = trusted
	s.out.emit(unpackedEvent{Event: "unpacked", ID: m.ID, Title: s.title, Entry: m.Entry, WorkDir: s.work.Dir,
		Code: nonNil(reasons), Trusted: trusted || s.o.TrustCode})
	if !trusted && !s.o.TrustCode {
		s.out.emit(confirmEvent{Event: "confirm", Question: app.CodeQuestion(file, s.title, s.work.Code)})
		switch s.awaitAnswer(ctx) {
		case cmdStop:
			return s.stop()
		case cmdNo:
			log.Printf("%s not opened: its code was not allowed to run", s.deckPath)
			s.cleanup()
			return ExitNotTrusted
		}
		if err := s.trustStore().Remember(m.ID, s.work.Hash, s.title); err != nil {
			log.Printf("remember that %s may run its code: %v", s.deckPath, err)
		}
		s.keepTrust = true
		// The question may have been up for longer than a claim counts as starting, and
		// another launch may have taken the talk over meanwhile.
		if err := s.handle.Renew(); errors.Is(err, instance.ErrNotOwner) {
			s.cleanup()
			s.out.emit(busyEvent{Event: "busy", BaseURL: s.currentBaseURL()})
			return ExitBusy
		} else if err != nil {
			log.Printf("renew the claim on %s: %v", s.deckPath, err)
		}
	}

	// Start Slidev, with the editor's addons; stdin can stop it while it starts.
	nodeArgs, env, err := slidev.AddonLaunch(s.home, s.o.Addons)
	if err != nil {
		return s.fail("Couldn't load the editor's addons: "+err.Error(), "")
	}
	s.logPath = filepath.Join(s.home, "logs", save.JournalKey(m.ID, s.deckPath)+".log")
	if err := os.MkdirAll(filepath.Dir(s.logPath), 0o755); err != nil {
		return s.fail("The talk couldn't be started: "+err.Error(), "")
	}
	cfg := slidev.Config{
		Node: nodePath(s.home), CLI: filepath.Join(s.rtDir, "node_modules", "@slidev", "cli", "bin", "slidev.mjs"),
		WorkDir: s.work.Dir, Entry: m.Entry, LogPath: s.logPath, NodeArgs: nodeArgs, Env: env,
	}
	srv, code, ok := s.start(ctx, cfg)
	if !ok {
		return code
	}
	if err := s.handle.Update(srv.Port(), srv.BaseURL(), s.work.Dir); err != nil {
		log.Printf("record the running instance: %v", err)
	}
	s.out.emit(readyEvent{Event: "ready", Port: srv.Port(), BaseURL: srv.BaseURL(), WorkDir: s.work.Dir})

	for {
		select {
		case <-ctx.Done():
			return s.stop()
		case err := <-srv.Done():
			msg := "Slidev stopped unexpectedly"
			if err != nil {
				msg += ": " + err.Error()
			}
			return s.fail(msg, slidev.LogTail(s.logPath))
		case c, open := <-s.cmds:
			switch {
			case !open || c.kind == cmdStop:
				return s.stop()
			case c.kind == cmdSave:
				s.save(ctx)
			case c.kind == cmdYes || c.kind == cmdNo:
				// No question is pending.
			default:
				s.unknown(c)
			}
		}
	}
}

// awaitAnswer waits for the answer to the question: cmdYes, cmdNo, or cmdStop for a stop, the
// end of stdin or the context's end.
func (s *session) awaitAnswer(ctx context.Context) cmdKind {
	for {
		select {
		case <-ctx.Done():
			return cmdStop
		case c, open := <-s.cmds:
			switch {
			case !open:
				return cmdStop
			case c.kind == cmdYes || c.kind == cmdNo || c.kind == cmdStop:
				return c.kind
			default:
				s.beforeReady(c)
			}
		}
	}
}

// start starts Slidev (startWithRetries) while it handles commands. ok is false when it was
// stopped or failed meanwhile; code is then Run's exit code, and everything is cleaned up.
func (s *session) start(ctx context.Context, cfg slidev.Config) (srv Server, code int, ok bool) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		srv Server
		err error
	}
	res := make(chan result, 1)
	go func() {
		srv, err := s.startWithRetries(sctx, cfg)
		res <- result{srv, err}
	}()
	stopping := false
	cmds, ctxDone := s.cmds, ctx.Done()
	for {
		select {
		case r := <-res:
			s.srv = r.srv
			if stopping || ctx.Err() != nil {
				return nil, s.stop(), false
			}
			if r.err != nil {
				return nil, s.fail("The talk couldn't be started: "+firstLine(r.err.Error()), slidev.LogTail(s.logPath)), false
			}
			return r.srv, 0, true
		case <-ctxDone:
			ctxDone = nil // sctx has ended too: the result follows
		case c, open := <-cmds:
			if !open || c.kind == cmdStop {
				stopping = true
				cancel()
				cmds = nil // the result is all that is waited for now
				continue
			}
			s.beforeReady(c)
		}
	}
}

// startWithRetries starts Slidev on a free port, up to maxStarts times when it exits before it
// answers (a lost port race).
func (s *session) startWithRetries(ctx context.Context, cfg slidev.Config) (Server, error) {
	var err error
	for i := 1; i <= maxStarts; i++ {
		var port int
		if port, err = slidev.FreePort(); err != nil {
			return nil, err
		}
		var srv Server
		if srv, err = s.d.Start(ctx, cfg, port); err == nil {
			return srv, nil
		}
		if ctx.Err() != nil || !strings.Contains(err.Error(), exitedEarly) {
			return nil, err
		}
		log.Printf("start %s on port %d (attempt %d of %d): %s", s.deckPath, port, i, maxStarts, firstLine(err.Error()))
	}
	return nil, err
}

// beforeReady handles a command that came before Slidev is ready, other than an answer to a
// question or a stop.
func (s *session) beforeReady(c command) {
	switch c.kind {
	case cmdSave:
		s.out.emit(saveFailedEvent{Event: "save-failed", Message: notOpenMessage, Names: []string{}})
	case cmdYes, cmdNo:
		// No question is pending.
	default:
		s.unknown(c)
	}
}

func (s *session) unknown(c command) {
	s.out.emit(errorEvent{Event: "error", Message: "Unknown command: " + c.line, Log: ""})
}

// save writes the work folder into the deck (workspace.SaveFolder) and works out the saved
// version's code and hash, as the next open will. A failed save is reported, and serving goes on.
func (s *session) save(ctx context.Context) {
	err := workspace.SaveFolder(&s.work, s.deckPath, s.d.Now().UTC())
	var conflict *workspace.ConflictError
	var bad *workspace.BadNamesError
	switch {
	case errors.As(err, &conflict):
		log.Printf("save %s: %v", s.deckPath, err)
		s.out.emit(messageEvent{Event: "conflict", Message: conflictMessage})
		return
	case errors.As(err, &bad):
		s.out.emit(saveFailedEvent{Event: "save-failed", Message: err.Error(), Names: nonNil(bad.Names)})
		return
	case err != nil:
		log.Printf("save %s: %v", s.deckPath, err)
		s.out.emit(saveFailedEvent{Event: "save-failed", Message: err.Error(), Names: []string{}})
		return
	}
	s.work.Rescan()
	s.check.Assess(ctx, &s.work, s.deckPath)
	if s.keepTrust && s.work.HasCode() {
		if err := s.trustStore().Remember(s.work.Manifest.ID, s.work.Hash, s.title); err != nil {
			log.Printf("remember that %s may run its code: %v", s.deckPath, err)
		}
	}
	s.out.emit(savedEvent{Event: "saved", Updated: s.work.Manifest.Updated.Format(time.RFC3339Nano)})
}

// cleanup stops Slidev, removes the work folder and gives up the claim, in that order: Slidev
// is not writing into the folder while it is removed, and no other launch takes the deck before
// it is gone. Safe to call more than once.
func (s *session) cleanup() {
	if s.srv != nil {
		if err := s.srv.Stop(); err != nil {
			log.Printf("stop Slidev: %v", err)
		}
		s.srv = nil
	}
	if s.work.Dir != "" {
		if err := workspace.Remove(s.work); err != nil {
			log.Printf("remove the work folder: %v", err)
		}
		s.work.Dir = ""
	}
	if s.handle != nil {
		s.handle.Release()
		s.handle = nil
	}
}

// stop cleans up and says so.
func (s *session) stop() int {
	s.cleanup()
	s.out.emit(stoppedEvent{Event: "stopped"})
	return ExitStopped
}

// fail reports msg and logTail, then cleans up and says so.
func (s *session) fail(msg, logTail string) int {
	log.Printf("serve %s: %s", s.o.DeckPath, msg)
	s.out.emit(errorEvent{Event: "error", Message: msg, Log: logTail})
	s.cleanup()
	s.out.emit(stoppedEvent{Event: "stopped"})
	return ExitError
}

func (s *session) trustStore() trust.Store {
	return trust.Store{Path: filepath.Join(s.home, trust.FileName)}
}

// currentBaseURL is the address in the deck's instance record, if any.
func (s *session) currentBaseURL() string {
	for _, r := range instance.List(s.home) {
		if instance.PathKey(r.DeckPath, runtime.GOOS) == instance.PathKey(s.deckPath, runtime.GOOS) {
			return r.BaseURL
		}
	}
	return ""
}

// nodePath is the bundled node program.
func nodePath(home string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(home, "node", "node.exe")
	}
	return filepath.Join(home, "node", "bin", "node")
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// Events, in the order of their fields on the line.
type (
	unpackedEvent struct {
		Event   string   `json:"event"`
		ID      string   `json:"id"`
		Title   string   `json:"title"`
		Entry   string   `json:"entry"`
		WorkDir string   `json:"workDir"`
		Code    []string `json:"code"`
		Trusted bool     `json:"trusted"`
	}
	confirmEvent struct {
		Event    string `json:"event"`
		Question string `json:"question"`
	}
	readyEvent struct {
		Event   string `json:"event"`
		Port    int    `json:"port"`
		BaseURL string `json:"baseUrl"`
		WorkDir string `json:"workDir"`
	}
	savedEvent struct {
		Event   string `json:"event"`
		Updated string `json:"updated"`
	}
	messageEvent struct {
		Event   string `json:"event"`
		Message string `json:"message"`
	}
	saveFailedEvent struct {
		Event   string   `json:"event"`
		Message string   `json:"message"`
		Names   []string `json:"names"`
	}
	busyEvent struct {
		Event   string `json:"event"`
		BaseURL string `json:"baseUrl"`
	}
	errorEvent struct {
		Event   string `json:"event"`
		Message string `json:"message"`
		Log     string `json:"log"`
	}
	stoppedEvent struct {
		Event string `json:"event"`
	}
)

// emitter writes events to stdout, one JSON object per line, each in one write and flushed.
type emitter struct {
	mu sync.Mutex
	w  io.Writer
}

func (e *emitter) emit(v any) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // the question's quotes and <…> stay readable
	if err := enc.Encode(v); err != nil {
		log.Printf("event %+v: %v", v, err)
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.w.Write(b.Bytes()); err != nil {
		log.Printf("write event: %v", err) // the editor is gone; the end of stdin stops Run
		return
	}
	if f, ok := e.w.(interface{ Flush() error }); ok {
		f.Flush()
	}
}

type cmdKind int

const (
	cmdUnknown cmdKind = iota
	cmdYes
	cmdNo
	cmdSave
	cmdStop
)

// command is one line of stdin.
type command struct {
	kind cmdKind
	line string // as it came, without the line end
}

// readCommands reads in line by line until its end, and sends the commands until done closes.
// Blank lines are skipped. The channel is closed at the end of in, or when reading fails.
func readCommands(in io.Reader, done <-chan struct{}) <-chan command {
	ch := make(chan command)
	go func() {
		defer close(ch)
		r := bufio.NewReader(in)
		for {
			line, err := r.ReadString('\n')
			if line = strings.TrimRight(line, "\r\n"); strings.TrimSpace(line) != "" {
				select {
				case ch <- parseCommand(line):
				case <-done:
					return
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) && !errors.Is(err, os.ErrClosed) {
					log.Printf("read commands: %v", err)
				}
				return
			}
		}
	}()
	return ch
}

// parseCommand reads one of {"answer":"yes"}, {"answer":"no"}, {"cmd":"save"} and
// {"cmd":"stop"}; anything else, other fields included, is cmdUnknown.
func parseCommand(line string) command {
	c := command{line: line}
	var v struct {
		Answer *string `json:"answer"`
		Cmd    *string `json:"cmd"`
	}
	dec := json.NewDecoder(strings.NewReader(line))
	dec.DisallowUnknownFields()
	if dec.Decode(&v) != nil {
		return c
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return c // more after the object
	}
	switch {
	case v.Answer != nil && v.Cmd == nil:
		switch *v.Answer {
		case "yes":
			c.kind = cmdYes
		case "no":
			c.kind = cmdNo
		}
	case v.Cmd != nil && v.Answer == nil:
		switch *v.Cmd {
		case "save":
			c.kind = cmdSave
		case "stop":
			c.kind = cmdStop
		}
	}
	return c
}
