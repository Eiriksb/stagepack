//go:build e2e

// Package test holds the end-to-end test: the real viewer binary opening a
// packed deck with the Linux build in dist/linux.
package test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
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

// linuxBuild returns dist/linux and its runtime folder, or skips the test when they are missing.
func linuxBuild(t *testing.T) (home, rt string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the end-to-end test runs the Linux build in dist/linux")
	}
	home, err := filepath.Abs(filepath.Join("..", "dist", "linux"))
	if err != nil {
		t.Fatal(err)
	}
	rt = filepath.Join(home, "runtimes", fmt.Sprint(slidevrt.Major()))
	for _, p := range []string{
		filepath.Join(home, "node", "bin", "node"),
		filepath.Join(rt, "node_modules", "@slidev", "cli", "bin", "slidev.mjs"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("%s is missing (build it with: make runtime node OS=linux): skipping", p)
		}
	}
	return home, rt
}

// buildViewer builds the viewer into dir and returns its path.
func buildViewer(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, strings.ToLower(brand.Name))
	build := exec.Command("go", "build", "-o", bin, "../cmd/stagepack")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

// browserScript writes a BROWSER program that appends its URL to urlFile, and returns its path.
func browserScript(t *testing.T, dir, urlFile string) string {
	t.Helper()
	script := filepath.Join(dir, "browser.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$1\" >> \""+urlFile+"\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestE2ENotesTravel(t *testing.T) {
	home, rt := linuxBuild(t)
	workBefore := listDir(t, filepath.Join(rt, "work"))

	tmp := t.TempDir()
	bin := buildViewer(t, tmp)

	// 1. Pack the fixture.
	deckPath := filepath.Join(tmp, "talk"+brand.Ext)
	m, err := pack.Pack(pack.Options{Dir: filepath.Join("..", "testdata", "deck"), Out: deckPath, Warn: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(filepath.Join(home, "logs", save.JournalKey(m.ID, deckPath)+".log")) })

	// 2. Open it headless; BROWSER records the URL instead of opening one.
	urlFile := filepath.Join(tmp, "url.txt")
	script := browserScript(t, tmp, urlFile)
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

// TestE2ECodeDeck: a talk whose vite.config.ts runs is not started where
// nobody can be asked, and runs, config and all, with --trust-code.
func TestE2ECodeDeck(t *testing.T) {
	home, rt := linuxBuild(t)
	workBefore := listDir(t, filepath.Join(rt, "work"))
	tmp := t.TempDir()
	bin := buildViewer(t, tmp)

	src := filepath.Join(tmp, "src")
	if err := os.CopyFS(src, os.DirFS(filepath.Join("..", "testdata", "deck"))); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(tmp, "config-ran")
	config := fmt.Sprintf("import { writeFileSync } from 'node:fs'\nwriteFileSync(%q, 'ran')\nexport default {}\n", marker)
	if err := os.WriteFile(filepath.Join(src, "vite.config.ts"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	deckPath := filepath.Join(tmp, "code"+brand.Ext)
	m, err := pack.Pack(pack.Options{Dir: src, Out: deckPath, Warn: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(filepath.Join(home, "logs", save.JournalKey(m.ID, deckPath)+".log")) })
	urlFile := filepath.Join(tmp, "url.txt")
	env := append(os.Environ(), "BROWSER="+browserScript(t, tmp, urlFile))

	// 1. Headless, nobody to ask: not opened, nothing run, nothing left.
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, "open", "--headless", "--home", home, deckPath)
	cmd.Env, cmd.Stdout, cmd.Stderr = env, &stdout, &stderr
	err = cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("open without --trust-code: %v, want exit 1\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"event":"confirm"`) || !strings.Contains(stdout.String(), "vite.config.ts") ||
		!strings.Contains(stdout.String(), `"answer":"no"`) {
		t.Errorf("no confirm line naming vite.config.ts:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "--trust-code") {
		t.Errorf("stderr does not mention --trust-code:\n%s", stderr.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("the talk's vite.config.ts ran though it was not allowed (%v)", err)
	}
	if _, err := os.Stat(urlFile); !os.IsNotExist(err) {
		t.Errorf("a browser was opened (%v)", err)
	}
	if left := newEntries(listDir(t, filepath.Join(rt, "work")), workBefore); len(left) != 0 {
		t.Errorf("work folders left: %v", left)
	}

	// 2. With --trust-code it runs, its config included.
	stdout.Reset()
	stderr.Reset()
	cmd = exec.Command(bin, "open", "--headless", "--trust-code", "--home", home, deckPath)
	cmd.Env, cmd.Stdout, cmd.Stderr = env, &stdout, &stderr
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
	waitFor(t, 3*time.Minute, "the browser URL", func() bool {
		data, err := os.ReadFile(urlFile)
		return err == nil && strings.TrimSpace(string(data)) != ""
	})
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the talk's vite.config.ts did not run with --trust-code: %v", err)
	}
	if strings.Contains(stdout.String(), `"event":"confirm"`) {
		t.Errorf("asked though --trust-code was given:\n%s", stdout.String())
	}
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
}

// A talk with `theme: default` that bundles its own @slidev/theme-default, of only a package.json and an
// index.html, is asked about: Slidev finds the talk's copy before the runtime's and puts the script in its
// index.html into the page. Pack never bundles a runtime package, so the entries are added to the zip after.
func TestE2EShadowedThemeDeck(t *testing.T) {
	home, rt := linuxBuild(t)
	workBefore := listDir(t, filepath.Join(rt, "work"))
	tmp := t.TempDir()
	bin := buildViewer(t, tmp)

	src := filepath.Join(tmp, "src")
	if err := os.CopyFS(src, os.DirFS(filepath.Join("..", "testdata", "deck"))); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(src, "slides.md")); err != nil || !strings.Contains(string(data), "\ntheme: default\n") {
		t.Fatalf("testdata/deck/slides.md does not set theme: default (%v)", err)
	}
	deckPath := filepath.Join(tmp, "theme"+brand.Ext)
	m, err := pack.Pack(pack.Options{Dir: src, Out: deckPath, Warn: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	const marker = "stagepack-shadowed-theme-ran"
	theme := "vendor/node_modules/@slidev/theme-default/"
	addEntries(t, deckPath, map[string]string{
		theme + "package.json": `{"name": "@slidev/theme-default", "version": "0.25.0"}`,
		theme + "index.html":   "<body><script>window['" + marker + "'] = 1</script></body>\n",
	})
	t.Cleanup(func() { os.Remove(filepath.Join(home, "logs", save.JournalKey(m.ID, deckPath)+".log")) })
	urlFile := filepath.Join(tmp, "url.txt")
	env := append(os.Environ(), "BROWSER="+browserScript(t, tmp, urlFile))

	// pageHolds fetches the talk's page from the URL the viewer opened and looks for the marker.
	pageHolds := func(url string) bool {
		t.Helper()
		resp, err := http.Get(url[:strings.Index(url, "/presenter")] + "/")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		page, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Contains(string(page), marker)
	}
	readURL := func() string {
		data, _ := os.ReadFile(urlFile)
		return strings.TrimSpace(string(data))
	}

	// 1. Headless: asked, the answer is no, nothing opened. Should it open instead, stop it there.
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, "open", "--headless", "--home", home, deckPath)
	cmd.Env, cmd.Stdout, cmd.Stderr = env, &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.Now().Add(3 * time.Minute)
	err = errors.New("it did not exit")
wait:
	for time.Now().Before(deadline) && readURL() == "" {
		select {
		case err = <-exited:
			break wait
		case <-time.After(100 * time.Millisecond):
		}
	}
	if cmd.ProcessState == nil {
		if url := readURL(); url != "" {
			t.Errorf("opened without asking; the bundled theme's script is in the page: %v", pageHolds(url))
		}
		cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(30 * time.Second):
			cmd.Process.Kill()
		}
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("open without --trust-code: %v, want exit 1\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"event":"confirm"`) || !strings.Contains(stdout.String(), "package @slidev/theme-default") {
		t.Errorf("no confirm line naming the bundled @slidev/theme-default:\n%s", stdout.String())
	}
	if _, err := os.Stat(urlFile); !os.IsNotExist(err) {
		t.Errorf("a browser was opened (%v)", err)
	}
	// Only this deck's folders: internal/slidev's tests may be using the work folder at the same time.
	for _, d := range newEntries(listDir(t, filepath.Join(rt, "work")), workBefore) {
		if _, err := os.Stat(filepath.Join(rt, "work", d, "node_modules", "@slidev", "theme-default", "index.html")); err == nil {
			t.Errorf("work folder left: %s", d)
		}
	}

	// 2. With --trust-code it opens, and the page holds the talk's script: the reason to ask was real.
	stdout.Reset()
	stderr.Reset()
	cmd = exec.Command(bin, "open", "--headless", "--trust-code", "--home", home, deckPath)
	cmd.Env, cmd.Stdout, cmd.Stderr = env, &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited = make(chan error, 1)
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
	waitFor(t, 3*time.Minute, "the browser URL", func() bool { return readURL() != "" })
	if !pageHolds(readURL()) {
		t.Error("the page does not hold the bundled theme's script")
	}
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
}

// addEntries rewrites the deck at deckPath with files added after its own entries.
func addEntries(t *testing.T, deckPath string, files map[string]string) {
	t.Helper()
	zr, err := zip.OpenReader(deckPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		if err := zw.Copy(f); err != nil {
			t.Fatal(err)
		}
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		w, err := zw.Create(name)
		if err == nil {
			_, err = io.WriteString(w, files[name])
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(deckPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A talk that names the export addon opens without being asked about, also headless where the answer
// would be no, and the addon the runtime bundles stays out of the way: no endpoint, so no button.
func TestE2EExportAddonDeck(t *testing.T) {
	home, _ := linuxBuild(t)
	tmp := t.TempDir()
	bin := buildViewer(t, tmp)

	src := filepath.Join(tmp, "src")
	if err := os.CopyFS(src, os.DirFS(filepath.Join("..", "testdata", "deck"))); err != nil {
		t.Fatal(err)
	}
	slides := filepath.Join(src, "slides.md")
	data, err := os.ReadFile(slides)
	if err != nil {
		t.Fatal(err)
	}
	withAddon := strings.Replace(string(data), "\ntitle: ", "\naddons: ["+brand.AddonName+"]\ntitle: ", 1)
	if withAddon == string(data) {
		t.Fatal("testdata/deck/slides.md has no title: line to put addons: before")
	}
	if err := os.WriteFile(slides, []byte(withAddon), 0o644); err != nil {
		t.Fatal(err)
	}
	var notes bytes.Buffer
	deckPath := filepath.Join(tmp, "addon"+brand.Ext)
	m, err := pack.Pack(pack.Options{Dir: src, Out: deckPath, Warn: &notes})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(notes.String(), "contains code") {
		t.Errorf("pack notes the export addon as code:\n%s", notes.String())
	}
	t.Cleanup(func() { os.Remove(filepath.Join(home, "logs", save.JournalKey(m.ID, deckPath)+".log")) })
	urlFile := filepath.Join(tmp, "url.txt")

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, "open", "--headless", "--home", home, deckPath)
	cmd.Env = append(os.Environ(), "BROWSER="+browserScript(t, tmp, urlFile))
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
	var url string
	waitFor(t, 3*time.Minute, "the browser URL", func() bool {
		data, err := os.ReadFile(urlFile)
		url = strings.TrimSpace(string(data))
		return err == nil && url != ""
	})
	if strings.Contains(stdout.String(), `"event":"confirm"`) {
		t.Errorf("asked about the export addon:\n%s", stdout.String())
	}
	base := url[:strings.Index(url, "/presenter")]
	resp, err := http.Get(base + "/__stagepack/status")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "json") {
		t.Errorf("the export addon answers in the viewer (%s): it should do nothing there", ct)
	}
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
