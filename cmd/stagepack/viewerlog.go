package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
)

// maxLogSize: a viewer.log bigger than this when a run starts is moved to viewer.log.1.
const maxLogSize = 1 << 20

// startLog is startViewerLog; tests replace it.
var startLog = startViewerLog

// startViewerLog sends the standard logger to <home>/logs/viewer.log, and to stderr too
// when there is one (a Windows program without a console has none, so its log would
// otherwise go nowhere). stop closes the file and puts the logger back on stderr. If the
// file can't be opened, logging goes on to stderr as before.
func startViewerLog(home string) (stop func()) {
	f, err := openLogFile(filepath.Join(home, "logs", "viewer.log"), maxLogSize)
	if err != nil {
		log.Printf("viewer log: %v", err)
		return func() {}
	}
	var w io.Writer = f
	if _, err := os.Stderr.Stat(); err == nil {
		w = io.MultiWriter(f, quietWriter{os.Stderr})
	}
	log.SetOutput(w)
	return func() {
		log.SetOutput(os.Stderr)
		f.Close()
	}
}

// openLogFile opens p for appending, making its folder. A file bigger than max is
// first renamed to p+".1", replacing an older one.
func openLogFile(p string, max int64) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	if fi, err := os.Stat(p); err == nil && fi.Size() > max {
		os.Remove(p + ".1")
		os.Rename(p, p+".1") // fails while another viewer has it open on Windows: then it just grows
	}
	return os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
}

// quietWriter ignores write errors, so a broken stderr never stops the log file.
type quietWriter struct{ w io.Writer }

func (q quietWriter) Write(p []byte) (int, error) {
	q.w.Write(p)
	return len(p), nil
}
