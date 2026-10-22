package slidev

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"stagepack/internal/brand"
)

// The test binary doubles as a fake Slidev: Start runs it as "Node", and
// FAKE_SLIDEV_MODE says how it behaves. This keeps the unit tests free of Node.
const fakeEnv = "FAKE_SLIDEV_MODE"

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeEnv); mode != "" {
		fake(mode)
		return
	}
	os.Exit(m.Run())
}

func fakePort() int {
	for i, a := range os.Args {
		if a == "--port" && i+1 < len(os.Args) {
			p, _ := strconv.Atoi(os.Args[i+1])
			return p
		}
	}
	return 0
}

// fake modes: serve, serve-then-exit, deaf (serves, ignores SIGTERM), exit-early, never-ready.
func fake(mode string) {
	fmt.Println("fake slidev:", mode)
	switch mode {
	case "exit-early":
		fmt.Fprintln(os.Stderr, "boom from fake")
		os.Exit(3)
	case "never-ready":
		time.Sleep(time.Minute)
		return
	}
	if mode == "deaf" {
		signal.Ignore(syscall.SIGTERM)
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", fakePort()))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if mode == "serve-then-exit" {
		go func() { time.Sleep(600 * time.Millisecond); os.Exit(0) }()
	}
	_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
}

func fakeConfig(t *testing.T, mode string) Config {
	t.Helper()
	t.Setenv(fakeEnv, mode)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	return Config{Node: exe, CLI: "cli.mjs", WorkDir: dir, Entry: "slides.md",
		LogPath: filepath.Join(dir, "slidev.log"), ReadyTimeout: 10 * time.Second}
}

func TestArgs(t *testing.T) {
	c := Config{CLI: "/rt/slidev.mjs", Entry: "slides.md"}
	want := []string{"/rt/slidev.mjs", "./slides.md", "--port", "4567", "--open", "false", "--log", "warn"}
	if got := Args(c, 4567); !reflect.DeepEqual(got, want) {
		t.Fatalf("Args = %q, want %q", got, want)
	}
}

func TestFreePortUsable(t *testing.T) {
	p, err := FreePort()
	if err != nil {
		t.Fatal(err)
	}
	if p < 1024 || p > 65535 {
		t.Fatalf("port %d out of range", p)
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
	if err != nil {
		t.Fatalf("cannot bind %d: %v", p, err)
	}
	ln.Close()
}

func TestStartFailsWithLog(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "cli.sh")
	if err := os.WriteFile(script, []byte("echo boom\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := Config{Node: "sh", CLI: script, WorkDir: dir, Entry: "slides.md", LogPath: filepath.Join(dir, "log.txt")}
	port, _ := FreePort()
	begin := time.Now()
	s, err := Start(context.Background(), c, port)
	if err == nil {
		s.Stop()
		t.Fatal("Start succeeded, want error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error does not carry the log: %v", err)
	}
	if time.Since(begin) > 5*time.Second {
		t.Fatalf("took %v", time.Since(begin))
	}
}

func TestStartKeepsLastTwentyLines(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "cli.sh")
	body := "i=1\nwhile [ $i -le 50 ]; do echo line$i; i=$((i+1)); done\nexit 1\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	c := Config{Node: "sh", CLI: script, WorkDir: dir, Entry: "slides.md", LogPath: filepath.Join(dir, "log.txt")}
	port, _ := FreePort()
	_, err := Start(context.Background(), c, port)
	if err == nil {
		t.Fatal("want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "line50") || !strings.Contains(msg, "line31") || strings.Contains(msg, "line30\n") || strings.HasSuffix(msg, "line30") {
		t.Fatalf("want exactly lines 31..50 in:\n%s", msg)
	}
}

func TestStartServesAndStops(t *testing.T) {
	c := fakeConfig(t, "serve")
	port, _ := FreePort()
	s, err := Start(context.Background(), c, port)
	if err != nil {
		t.Fatal(err)
	}
	if s.Port() != port {
		t.Fatalf("Port = %d, want %d", s.Port(), port)
	}
	if !strings.HasPrefix(s.BaseURL(), "http://") {
		t.Fatalf("BaseURL = %q", s.BaseURL())
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := s.Stop(); err != nil { // idempotent
		t.Fatalf("second Stop: %v", err)
	}
	select {
	case err := <-s.Done():
		if err != nil {
			t.Fatalf("Done after Stop = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Done never delivered")
	}
}

func TestStopAfterSelfExit(t *testing.T) {
	c := fakeConfig(t, "serve-then-exit")
	port, _ := FreePort()
	s, err := Start(context.Background(), c, port)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done did not fire after the process exited by itself")
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop after exit: %v", err)
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop again: %v", err)
	}
}

func TestStopEscalatesToKill(t *testing.T) {
	old := stopGrace
	stopGrace = 300 * time.Millisecond
	defer func() { stopGrace = old }()
	c := fakeConfig(t, "deaf")
	port, _ := FreePort()
	s, err := Start(context.Background(), c, port)
	if err != nil {
		t.Fatal(err)
	}
	begin := time.Now()
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if d := time.Since(begin); d < 250*time.Millisecond || d > 4*time.Second {
		t.Fatalf("Stop took %v, want about the grace period", d)
	}
}

func TestStartReadyTimeout(t *testing.T) {
	c := fakeConfig(t, "never-ready")
	c.ReadyTimeout = 800 * time.Millisecond
	port, _ := FreePort()
	_, err := Start(context.Background(), c, port)
	if err == nil || !strings.Contains(err.Error(), "fake slidev: never-ready") {
		t.Fatalf("want timeout error with the log, got %v", err)
	}
}

func TestStartContextCancel(t *testing.T) {
	c := fakeConfig(t, "never-ready")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	port, _ := FreePort()
	begin := time.Now()
	_, err := Start(ctx, c, port)
	if err == nil || time.Since(begin) > 5*time.Second {
		t.Fatalf("want prompt cancellation error, got %v after %v", err, time.Since(begin))
	}
}

func TestStartExitsEarlyReportsLog(t *testing.T) {
	c := fakeConfig(t, "exit-early")
	port, _ := FreePort()
	_, err := Start(context.Background(), c, port)
	if err == nil || !strings.Contains(err.Error(), "boom from fake") {
		t.Fatalf("got %v", err)
	}
}

func TestChildEnv(t *testing.T) {
	has := func(env []string, want string) int {
		n := 0
		for _, kv := range env {
			if kv == want {
				n++
			}
		}
		return n
	}
	env := childEnv([]string{"A=1", "BROWSER=firefox", "NODE_OPTIONS=--max-old-space-size=4096"})
	if has(env, "A=1") != 1 || has(env, "BROWSER=none") != 1 || has(env, "BROWSER=firefox") != 0 || has(env, brand.ViewerEnv+"=1") != 1 {
		t.Fatalf("env = %q", env)
	}
	if has(env, "NODE_OPTIONS=--max-old-space-size=4096 --dns-result-order=ipv4first") != 1 {
		t.Fatalf("NODE_OPTIONS not appended: %q", env)
	}
	env = childEnv([]string{"A=1"})
	if has(env, "NODE_OPTIONS=--dns-result-order=ipv4first") != 1 {
		t.Fatalf("NODE_OPTIONS not set: %q", env)
	}
	env = childEnv([]string{"NODE_OPTIONS=--dns-result-order=ipv4first"})
	if has(env, "NODE_OPTIONS=--dns-result-order=ipv4first") != 1 {
		t.Fatalf("flag duplicated: %q", env)
	}
}
