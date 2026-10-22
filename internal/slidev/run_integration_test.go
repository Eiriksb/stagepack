//go:build e2e

package slidev_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"stagepack/internal/brand"
	"stagepack/internal/pack"
	"stagepack/internal/slidev"
	"stagepack/internal/workspace"
)

func TestStartStopIntegration(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the integration test runs the Linux build in dist/linux")
	}
	home, err := filepath.Abs(filepath.Join("..", "..", "dist", "linux"))
	if err != nil {
		t.Fatal(err)
	}
	node := filepath.Join(home, "node", "bin", "node")
	rt := filepath.Join(home, "runtimes", "53")
	cli := filepath.Join(rt, "node_modules", "@slidev", "cli", "bin", "slidev.mjs")
	for _, p := range []string{node, cli} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("%s is missing (build it with: make runtime node OS=linux): skipping", p)
		}
	}

	deckPath := filepath.Join(t.TempDir(), "talk"+brand.Ext)
	if _, err := pack.Pack(pack.Options{Dir: filepath.Join("..", "..", "testdata", "deck"), Out: deckPath, Warn: io.Discard}); err != nil {
		t.Fatal(err)
	}
	work, err := workspace.Extract(deckPath, rt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { workspace.Remove(work) })

	port, err := slidev.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	c := slidev.Config{Node: node, CLI: cli, WorkDir: work.Dir, Entry: work.Manifest.Entry,
		LogPath: filepath.Join(t.TempDir(), "slidev.log")}
	s, err := slidev.Start(context.Background(), c, port)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Stop() })
	t.Logf("ready at %s", s.BaseURL())

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/presenter/", port))
	if err != nil {
		t.Logf("127.0.0.1 does not answer: %v", err)
		resp, err = http.Get(fmt.Sprintf("http://localhost:%d/presenter/", port))
		if err != nil {
			t.Fatal(err)
		}
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("/presenter/ = %d", resp.StatusCode)
	}

	// Prove the process check can fail: it must see Slidev while it runs.
	if running := procsInDir(t, work.Dir); len(running) == 0 {
		t.Fatal("cwd scan finds no process in the work dir while Slidev runs; the check is blind")
	} else {
		t.Logf("running before Stop: %v", running)
	}

	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(6 * time.Second)
	for {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			ln.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("port %d still busy 6 s after Stop: %v", port, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	deadline = time.Now().Add(6 * time.Second)
	for {
		left := procsInDir(t, work.Dir)
		if len(left) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("processes still have the work dir as cwd 6 s after Stop: %v", left)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// procsInDir lists pids (with command) whose cwd is dir or below it, read from
// /proc/*/cwd. Processes we cannot read are ignored.
func procsInDir(t *testing.T, dir string) []string {
	t.Helper()
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	links, _ := filepath.Glob("/proc/[0-9]*/cwd")
	var found []string
	for _, l := range links {
		cwd, err := os.Readlink(l)
		if err != nil {
			continue
		}
		if r, err := filepath.EvalSymlinks(cwd); err == nil {
			cwd = r
		}
		if cwd == want || strings.HasPrefix(cwd, want+string(filepath.Separator)) {
			pid := filepath.Base(filepath.Dir(l))
			cmdline, _ := os.ReadFile(filepath.Join(filepath.Dir(l), "cmdline"))
			found = append(found, pid+" "+strings.ReplaceAll(string(cmdline), "\x00", " "))
		}
	}
	return found
}
