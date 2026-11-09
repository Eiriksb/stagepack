//go:build e2e

package test

import (
	"archive/zip"
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"stagepack/internal/brand"
	"stagepack/internal/instance"
	"stagepack/internal/pack"
	"stagepack/internal/save"
)

// TestServeE2E runs dist/linux/stagepack (make build) as an editor would: serve --json with the
// probe addon on a packed testdata/deck at an odd path, an edit saved through stdin, and a stop
// by closing stdin.
func TestServeE2E(t *testing.T) {
	home, _ := linuxBuild(t)
	bin := filepath.Join(home, strings.ToLower(brand.Name))
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("%s is missing (build it with: make build): skipping", bin)
	}
	probe, err := filepath.Abs(filepath.Join("..", "testdata", "addons", "probe"))
	if err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "a b%c")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	deckPath := filepath.Join(dir, "my talk æøå 100%"+brand.Ext)
	m, err := pack.Pack(pack.Options{Dir: filepath.Join("..", "testdata", "deck"), Out: deckPath, Warn: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(filepath.Join(home, "logs", save.JournalKey(m.ID, deckPath)+".log")) })

	cmd := exec.Command(bin, "serve", "--json", "--home", home, "--addon", probe, deckPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var transcript []string // stdout, for the log
	events := make(chan map[string]any, 16)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := sc.Text()
			mu.Lock()
			transcript = append(transcript, line)
			mu.Unlock()
			var ev map[string]any
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				ev = map[string]any{"event": "(not JSON)", "line": line}
			}
			events <- ev
		}
		close(events)
	}()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() {
		if cmd.ProcessState == nil {
			cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-exited:
			case <-time.After(30 * time.Second):
				cmd.Process.Kill()
			}
		}
		mu.Lock()
		defer mu.Unlock()
		t.Logf("stdout:\n%s\nstderr:\n%s", strings.Join(transcript, "\n"), stderr.String())
	}()
	next := func(want string, within time.Duration) map[string]any {
		t.Helper()
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("stdout ended; want %s", want)
			}
			if ev["event"] != want {
				t.Fatalf("event %v, want %s", ev, want)
			}
			return ev
		case <-time.After(within):
			t.Fatalf("no %s within %v", want, within)
		}
		return nil
	}

	unpacked := next("unpacked", time.Minute)
	if unpacked["trusted"] != true {
		t.Fatalf("testdata/deck counts as code (%v): this test needs --trust-code", unpacked["code"])
	}
	ready := next("ready", 3*time.Minute)
	base, _ := ready["baseUrl"].(string)
	workDir, _ := ready["workDir"].(string)
	if base == "" || workDir != unpacked["workDir"] {
		t.Fatalf("ready = %v, unpacked = %v", ready, unpacked)
	}

	// Slidev serves the talk, with the probe addon loaded.
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	get := func(path string) (int, string) {
		t.Helper()
		resp, err := client.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(data)
	}
	if code, _ := get("/"); code != http.StatusOK {
		t.Errorf("GET / = %d, want 200", code)
	}
	if code, body := get("/__probe-addon"); code != http.StatusOK || body != "ok" {
		t.Errorf("GET /__probe-addon = %d %q, want 200 \"ok\"", code, body)
	}
	// Another viewer sees the talk as open.
	if existing, h, err := instance.Claim(home, deckPath, instance.Alive); err != nil || existing == nil || existing.BaseURL != base {
		if h != nil {
			h.Release()
		}
		t.Errorf("a second claim got %+v, %v; want serve's record at %s", existing, err, base)
	}

	// An edit, saved on command.
	slides := filepath.Join(workDir, "slides.md")
	data, err := os.ReadFile(slides)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(data), "Hello from the fixture.", "Hello from the editor, æøå 100%.", 1)
	if edited == string(data) {
		t.Fatal("the fixture's slides changed: no line to edit")
	}
	if err := os.WriteFile(slides, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(stdin, `{"cmd":"save"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	next("saved", time.Minute)

	// Closing stdin stops it.
	stdin.Close()
	next("stopped", time.Minute)
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("serve exited with %v, want 0", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("serve did not exit after stopped")
	}
	if ev, ok := <-events; ok {
		t.Errorf("event after stopped: %v", ev)
	}

	if got := string(entry(t, deckPath, "slides.md")); got != edited {
		t.Errorf("the deck's slides.md = %q, want the edit", got)
	}
	zr, err := zip.OpenReader(deckPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if strings.Contains(f.Name, "probe") || strings.Contains(f.Name, "addons-preload") {
			t.Errorf("the deck holds %s, which the editor brought", f.Name)
		}
	}
	zr.Close()
	if _, err := os.Stat(workDir); !os.IsNotExist(err) {
		t.Errorf("the work folder is still there: %v", err)
	}
	for _, r := range instance.List(home) {
		if instance.PathKey(r.DeckPath, "linux") == instance.PathKey(deckPath, "linux") {
			t.Errorf("the instance record is still there: %+v", r)
		}
	}
}
