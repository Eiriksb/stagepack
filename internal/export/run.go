// Package export runs slidev-pptx-export's command line and reads its progress.
package export

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	slidevrt "stagepack/runtime"
)

// Paths are the programs the exporter needs, all under the app's home folder.
type Paths struct{ Node, CLI, Browser, FFmpeg string }

// PathsFor lays out the files under home for the given GOOS.
func PathsFor(home, goos string) Paths {
	exe := ""
	if goos == "windows" {
		exe = ".exe"
	}
	return Paths{
		Node:    filepath.Join(home, "node", "node"+exe),
		CLI:     filepath.Join(home, "runtimes", strconv.Itoa(slidevrt.Major()), "node_modules", "slidev-pptx-export", "bin", "cli.mjs"),
		Browser: filepath.Join(home, "browser-shell", "chrome-headless-shell"+exe),
		FFmpeg:  filepath.Join(home, "ffmpeg", "ffmpeg"+exe),
	}
}

// Job is one export: Slides is the slides file in the work folder.
type Job struct {
	Slides, Out string
	Animations  bool
}

// Event is one progress line from the exporter.
type Event struct {
	Event   string  `json:"event"`
	Slides  int     `json:"slides"`
	Slide   int     `json:"slide"`
	States  int     `json:"states"`
	State   int     `json:"state"`
	Click   int     `json:"click"`
	Clips   int     `json:"clips"`
	Seconds float64 `json:"seconds"`
	Bytes   int64   `json:"bytes"`
	Path    string  `json:"path"`
	Text    string  `json:"text"`
	Message string  `json:"message"`
}

// Command builds the exporter's command line, run in the slides file's folder.
func Command(p Paths, j Job) *exec.Cmd {
	args := []string{p.CLI, j.Slides, j.Out, "--render"}
	if j.Animations {
		args = append(args, "--animations")
	}
	args = append(args, "--progress", "json")
	cmd := exec.Command(p.Node, args...)
	cmd.Dir = filepath.Dir(j.Slides)
	cmd.Env = append(os.Environ(), "SLIDEV_PPTX_BROWSER="+p.Browser, "SLIDEV_PPTX_FFMPEG="+p.FFmpeg)
	return cmd
}

const stopGrace = 5 * time.Second

// tail keeps the last bytes written to it.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tail) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if len(t.buf) > 4096 {
		t.buf = t.buf[len(t.buf)-4096:]
	}
	return len(b), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(bytes.ToValidUTF8(t.buf, nil)))
}

// Run starts cmd in its own process group (job object on Windows) and calls
// onEvent for each JSON line on its stdout. If ctx ends first, the group is
// stopped and context.Canceled (or ctx's error) is returned. A failing exit
// returns an error carrying the exporter's error message. out+".partial" is
// removed unless the exporter exited cleanly.
func Run(ctx context.Context, cmd *exec.Cmd, out string, onEvent func(Event)) (err error) {
	clean := false
	defer func() {
		if !clean {
			_ = os.Remove(out + ".partial")
		}
	}()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var errTail tail
	if cmd.Stderr == nil {
		cmd.Stderr = &errTail
	}
	prepare(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", cmd.Path, err)
	}
	proc, err := attach(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	exited := make(chan struct{})
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		select {
		case <-ctx.Done():
			_ = proc.stop(exited, stopGrace)
		case <-exited:
		}
	}()

	var failure string
	sawDone := false
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var ev Event
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Event == "" {
			continue
		}
		if ev.Event == "done" {
			sawDone = true
		}
		if ev.Event == "error" {
			failure = ev.Message
		}
		if onEvent != nil {
			onEvent(ev)
		}
	}
	scanErr := sc.Err()
	if scanErr != nil {
		// Keep reading so the exporter is not stuck on a full pipe and can finish.
		_, _ = io.Copy(io.Discard, stdout)
	}
	waitErr := cmd.Wait()
	proc.cleanup() // anything the leader left behind
	close(exited)
	<-watched

	if ctx.Err() != nil && !sawDone {
		return ctx.Err() // cancelled, even if the exporter exited 0 while stopping
	}
	if scanErr != nil {
		if errors.Is(scanErr, bufio.ErrTooLong) {
			return errors.New("the exporter printed a progress line that is too long")
		}
		return fmt.Errorf("reading the exporter's progress: %w", scanErr)
	}
	if waitErr == nil {
		clean = true
		return nil
	}
	switch {
	case failure != "":
		return errors.New(failure)
	case errTail.String() != "":
		return fmt.Errorf("the exporter failed (%v): %s", waitErr, errTail.String())
	}
	return fmt.Errorf("the exporter failed: %w", waitErr)
}
