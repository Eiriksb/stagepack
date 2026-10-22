// Package slidev starts and stops the bundled Slidev dev server for one deck.
package slidev

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"stagepack/internal/brand"
)

// Config says how to run Slidev.
type Config struct {
	Node         string        // the node executable
	CLI          string        // Slidev's bin/slidev.mjs
	WorkDir      string        // the extracted deck; Slidev runs here
	Entry        string        // slides file in WorkDir, normally slides.md
	LogPath      string        // Slidev's stdout and stderr go here (required)
	ReadyTimeout time.Duration // default 120 s: Vite's first dependency optimisation is slow
}

const (
	defaultReadyTimeout = 120 * time.Second
	pollInterval        = 200 * time.Millisecond
	logTailLines        = 20
)

// stopGrace is how long Stop waits after the polite request before it kills.
// A variable so tests can shorten it.
var stopGrace = 5 * time.Second

// Args returns the arguments after the node executable. Slidev is never given
// --remote, so it binds localhost only. The entry goes as "./<entry>", so it
// can never be read as an option.
func Args(c Config, port int) []string {
	return []string{c.CLI, "./" + c.Entry, "--port", strconv.Itoa(port), "--open", "false", "--log", "warn"}
}

// FreePort returns a port on the loopback interface that was free a moment ago.
func FreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// Server is a running Slidev.
type Server struct {
	port    int
	base    string
	proc    *procCtl
	done    chan error    // buffered(1): the exit result, sent once, then closed
	exited  chan struct{} // closed after done holds its value
	stopped atomic.Bool
	once    sync.Once
	stopErr error
}

// Port is the port Slidev listens on.
func (s *Server) Port() int { return s.port }

// BaseURL is the address that answered while waiting for readiness, such as
// "http://127.0.0.1:4567". Open the browser on it.
func (s *Server) BaseURL() string { return s.base }

// Done delivers one value when Slidev has exited, then is closed: the exit
// error, or nil when it exited cleanly or because of Stop. It never blocks the
// process: nobody has to receive from it.
func (s *Server) Done() <-chan error { return s.done }

// Stop ends Slidev and everything it started, and returns once it is gone.
// It is safe to call more than once, concurrently, and after Slidev exited by
// itself.
func (s *Server) Stop() error {
	s.once.Do(func() {
		s.stopped.Store(true)
		select {
		case <-s.exited:
		default:
			s.stopErr = s.proc.stop(s.exited, stopGrace)
		}
	})
	<-s.exited
	return s.stopErr
}

// Start runs Slidev and returns when it answers, or fails with the end of its
// log. If ctx ends first, Slidev is stopped and ctx's error is returned.
func Start(ctx context.Context, c Config, port int) (*Server, error) {
	if c.LogPath == "" {
		return nil, errors.New("slidev: LogPath is required")
	}
	timeout := c.ReadyTimeout
	if timeout <= 0 {
		timeout = defaultReadyTimeout
	}
	logf, err := os.Create(c.LogPath)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(c.Node, Args(c, port)...)
	cmd.Dir = c.WorkDir
	cmd.Env = childEnv(os.Environ())
	cmd.Stdout, cmd.Stderr = logf, logf
	prepare(cmd)
	if err := cmd.Start(); err != nil {
		logf.Close()
		return nil, fmt.Errorf("start %s: %w", c.Node, err)
	}
	proc, err := attach(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		logf.Close()
		return nil, err
	}
	s := &Server{port: port, proc: proc, done: make(chan error, 1), exited: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		proc.cleanup() // anything the leader left behind
		logf.Close()
		if s.stopped.Load() {
			err = nil
		}
		s.done <- err
		close(s.done)
		close(s.exited)
	}()

	base, err := s.waitReady(ctx, timeout)
	if err != nil {
		s.Stop()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w\n%s", err, LogTail(c.LogPath))
	}
	s.base = base
	return s, nil
}

const ipv4First = "--dns-result-order=ipv4first"

// childEnv is base plus BROWSER=none, brand.ViewerEnv=1 (the export addon then stays out of
// the way) and NODE_OPTIONS with --dns-result-order=ipv4first added to any existing value.
// That makes Node resolve "localhost" to 127.0.0.1 first on every OS, so Slidev's
// "localhost" bind is IPv4 and 127.0.0.1 URLs always work.
func childEnv(base []string) []string {
	const key = "NODE_OPTIONS="
	env := make([]string, 0, len(base)+2)
	opts := ""
	for _, kv := range base {
		switch {
		case strings.HasPrefix(strings.ToUpper(kv), key): // env names are case-insensitive on Windows
			opts = kv[len(key):]
		case kv == "BROWSER" || strings.HasPrefix(kv, "BROWSER="):
		case strings.HasPrefix(strings.ToUpper(kv), brand.ViewerEnv+"="):
		default:
			env = append(env, kv)
		}
	}
	if !strings.Contains(opts, ipv4First) {
		opts = strings.TrimSpace(opts + " " + ipv4First)
	}
	return append(env, "BROWSER=none", brand.ViewerEnv+"=1", key+opts)
}

// probes are tried in turn each round. Slidev binds "localhost", which may be
// 127.0.0.1 or ::1 depending on the machine.
func probes(port int) []struct{ base, path string } {
	return []struct{ base, path string }{
		{fmt.Sprintf("http://127.0.0.1:%d", port), "/presenter/"},
		{fmt.Sprintf("http://localhost:%d", port), "/"},
	}
}

func (s *Server) waitReady(ctx context.Context, timeout time.Duration) (string, error) {
	client := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	for {
		for _, p := range probes(s.port) {
			resp, err := client.Get(p.base + p.path)
			if err != nil {
				continue
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return p.base, nil
			}
		}
		select {
		case <-s.exited:
			err := <-s.done // may be nil, if it exited with status 0
			if err == nil {
				err = errors.New("exited")
			}
			return "", fmt.Errorf("slidev exited before it was ready: %w", err)
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			return "", fmt.Errorf("slidev did not answer on port %d within %v", s.port, timeout)
		case <-tick.C:
		}
	}
}

// LogTail is the last lines of a Slidev log, for showing why it stopped.
func LogTail(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "(no log: " + err.Error() + ")"
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > logTailLines {
			lines = lines[1:]
		}
	}
	return strings.Join(lines, "\n")
}
