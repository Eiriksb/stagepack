//go:build e2e

// Package test holds the end-to-end test: the real viewer binary opening a
// packed deck with the Linux build in dist/linux.
package test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"stagepack/internal/brand"
	"stagepack/internal/deck"
	"stagepack/internal/instance"
	"stagepack/internal/pack"
	"stagepack/internal/save"
	slidevrt "stagepack/runtime"
)

func TestE2ENotesTravel(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the end-to-end test runs the Linux build in dist/linux")
	}
	home, err := filepath.Abs(filepath.Join("..", "dist", "linux"))
	if err != nil {
		t.Fatal(err)
	}
	rt := filepath.Join(home, "runtimes", fmt.Sprint(slidevrt.Major()))
	for _, p := range []string{
		filepath.Join(home, "node", "bin", "node"),
		filepath.Join(rt, "node_modules", "@slidev", "cli", "bin", "slidev.mjs"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("%s is missing (build it with: make runtime node OS=linux): skipping", p)
		}
	}
	workBefore := listDir(t, filepath.Join(rt, "work"))

	tmp := t.TempDir()
	bin := filepath.Join(tmp, strings.ToLower(brand.Name))
	build := exec.Command("go", "build", "-o", bin, "../cmd/stagepack")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	// 1. Pack the fixture.
	deckPath := filepath.Join(tmp, "talk"+brand.Ext)
	m, err := pack.Pack(pack.Options{Dir: filepath.Join("..", "testdata", "deck"), Out: deckPath, Warn: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(filepath.Join(home, "logs", save.JournalKey(m.ID, deckPath)+".log")) })

	// 2. Open it headless; BROWSER records the URL instead of opening one.
	urlFile := filepath.Join(tmp, "url.txt")
	script := filepath.Join(tmp, "browser.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$1\" >> \""+urlFile+"\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, "open", "--headless", "--home", home, deckPath)
	cmd.Env = append(os.Environ(), "BROWSER="+script)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
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
		t.Logf("stdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}()

	// 3. Wait for the presenter URL and a 200 from it.
	var presenter string
	deadline := time.Now().Add(3 * time.Minute)
	for presenter == "" {
		if data, err := os.ReadFile(urlFile); err == nil && strings.TrimSpace(string(data)) != "" {
			presenter = strings.TrimSpace(strings.SplitN(string(data), "\n", 2)[0])
			break
		}
		select {
		case err := <-exited:
			t.Fatalf("the viewer exited before opening the browser: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("no browser URL within 3 minutes")
		}
	}
	if !strings.HasPrefix(presenter, "http://127.0.0.1:") || !strings.HasSuffix(presenter, "/presenter/") {
		t.Fatalf("browser URL = %q, want http://127.0.0.1:<port>/presenter/", presenter)
	}
	resp, err := http.Get(presenter)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s = %d", presenter, resp.StatusCode)
	}

	// 4. Edit the first slide's note the way the presenter's note editor does.
	base := strings.TrimSuffix(presenter, "/presenter/")
	resp, err = http.Post(base+"/__slidev/slides/1.json", "application/json", strings.NewReader(`{"note":"edited in e2e"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST note = %d: %s", resp.StatusCode, body)
	}
	// Slidev writes slides.md in the work folder; wait until it has.
	var workDir string
	waitFor(t, 10*time.Second, "slides.md in the work folder to have the note", func() bool {
		for _, d := range newEntries(listDir(t, filepath.Join(rt, "work")), workBefore) {
			if data, err := os.ReadFile(filepath.Join(rt, "work", d, "slides.md")); err == nil && strings.Contains(string(data), "edited in e2e") {
				workDir = filepath.Join(rt, "work", d)
				return true
			}
		}
		return false
	})
	if len(procsIn(workDir)) == 0 {
		t.Fatal("no process found running in the work folder; the leftover check below would be blind")
	}

	// 5. SIGTERM, and wait for a clean exit.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("the viewer exited with %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the viewer did not exit within 60 s of SIGTERM")
	}

	// 6. The note is in the deck, the video is untouched, and nothing is left behind.
	if got := entry(t, deckPath, "slides.md"); !strings.Contains(string(got), "edited in e2e") {
		t.Errorf("slides.md in the deck has no edit:\n%s", got)
	}
	clip, err := os.ReadFile(filepath.Join("..", "testdata", "deck", "public", "clip.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if got := entry(t, deckPath, "public/clip.mp4"); !bytes.Equal(got, clip) {
		t.Error("public/clip.mp4 changed")
	}
	if left := procsIn(workDir); len(left) != 0 {
		t.Errorf("processes left in the work folder: %v", left)
	}
	if left := newEntries(listDir(t, filepath.Join(rt, "work")), workBefore); len(left) != 0 {
		t.Errorf("work folders left: %v", left)
	}
	recs, _ := filepath.Glob(filepath.Join(home, "instances", "*.json"))
	for _, r := range recs {
		var rec instance.Record
		if data, err := os.ReadFile(r); err == nil && json.Unmarshal(data, &rec) == nil && rec.DeckPath == deckPath {
			t.Errorf("instance record left: %s", r)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "journal", save.JournalKey(m.ID, deckPath)+".json")); !os.IsNotExist(err) {
		t.Errorf("journal entry left after a clean stop (%v)", err)
	}
}

// procsIn lists the pids whose working folder is dir or below it (also once dir is deleted).
func procsIn(dir string) []string {
	links, _ := filepath.Glob("/proc/[0-9]*/cwd")
	var found []string
	for _, l := range links {
		cwd, err := os.Readlink(l)
		if err != nil {
			continue
		}
		cwd = strings.TrimSuffix(cwd, " (deleted)")
		if cwd == dir || strings.HasPrefix(cwd, dir+"/") {
			found = append(found, filepath.Base(filepath.Dir(l)))
		}
	}
	return found
}

func waitFor(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

func newEntries(now, before []string) []string {
	old := map[string]bool{}
	for _, n := range before {
		old[n] = true
	}
	var out []string
	for _, n := range now {
		if !old[n] {
			out = append(out, n)
		}
	}
	return out
}

func entry(t *testing.T, deckPath, name string) []byte {
	t.Helper()
	zr, err := zip.OpenReader(deckPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	if _, err := deck.ReadManifest(&zr.Reader); err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	t.Fatalf("%s has no entry %s", deckPath, name)
	return nil
}
