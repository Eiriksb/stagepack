package export

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	slidevrt "stagepack/runtime"
)

// The test binary doubles as a fake exporter: Command runs it as "Node", and
// FAKE_EXPORT_MODE says how it behaves. os.Args is [binary, cli, slides, out, ...].
const fakeEnv = "FAKE_EXPORT_MODE"

// fakeStderrEnv is what the "stderr" mode writes to stderr.
const fakeStderrEnv = "FAKE_EXPORT_STDERR"

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeEnv); mode != "" {
		fake(mode)
		return
	}
	os.Exit(m.Run())
}

func fake(mode string) {
	out := os.Args[3]
	switch mode {
	case "events":
		println3(`{"event":"start","slides":2}`, `{"event":"clip","slide":1,"click":2,"seconds":1.5,"bytes":4096}`,
			`{"event":"done","path":"x.pptx","slides":2,"clips":1,"bytes":99}`)
	case "fail":
		println3(`{"event":"start","slides":2}`, `{"event":"error","message":"Slidev could not start"}`)
		_ = os.WriteFile(out+".partial", []byte("x"), 0o644)
		os.Exit(1)
	case "hang":
		_ = os.WriteFile(out+".partial", []byte("x"), 0o644)
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), fakeEnv+"=sleep")
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		_ = os.WriteFile(out+".pid", []byte(strconv.Itoa(child.Process.Pid)), 0o644)
		println3(`{"event":"start","slides":2}`)
		time.Sleep(time.Minute)
	case "term":
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		_ = os.WriteFile(out+".partial", []byte("x"), 0o644)
		println3(`{"event":"start","slides":2}`)
		<-ch
		os.Exit(0)
	case "longline":
		w := bufio.NewWriter(os.Stdout)
		w.WriteString(`{"event":"notice","text":"` + strings.Repeat("x", 5<<20) + `"}` + "\n")
		w.WriteString(`{"event":"done","path":"x.pptx"}` + "\n")
		w.Flush()
	case "sleep":
		time.Sleep(time.Minute)
	case "stderr": // a crash: a message on stderr alone, no error event
		println3(`{"event":"start","slides":2}`)
		os.Stderr.WriteString(os.Getenv(fakeStderrEnv) + "\n")
		os.Exit(1)
	}
}

func println3(lines ...string) {
	w := bufio.NewWriter(os.Stdout)
	for _, l := range lines {
		w.WriteString(l + "\n")
	}
	w.Flush()
}

func fakeCmd(t *testing.T, mode string) (*exec.Cmd, string) {
	t.Helper()
	t.Setenv(fakeEnv, mode)
	dir := t.TempDir()
	out := filepath.Join(dir, "deck.pptx")
	return Command(Paths{Node: os.Args[0], CLI: "cli.mjs"}, Job{Slides: filepath.Join(dir, "slides.md"), Out: out}), out
}

func TestCommandArgsAndEnv(t *testing.T) {
	p := Paths{Node: "/n/node", CLI: "/r/cli.mjs", Browser: "/b/chrome", FFmpeg: "/f/ffmpeg"}
	for _, anim := range []bool{false, true} {
		cmd := Command(p, Job{Slides: "/w/slides.md", Out: "/o/deck.pptx", Animations: anim})
		args := strings.Join(cmd.Args, " ")
		want := "/n/node /r/cli.mjs /w/slides.md /o/deck.pptx --render"
		if anim {
			want += " --animations"
		}
		want += " --progress json"
		if args != want {
			t.Errorf("animations=%v: args %q, want %q", anim, args, want)
		}
		if cmd.Dir != filepath.FromSlash("/w") {
			t.Errorf("Dir = %q", cmd.Dir)
		}
		env := strings.Join(cmd.Env, "\n")
		for _, kv := range []string{"SLIDEV_PPTX_BROWSER=/b/chrome", "SLIDEV_PPTX_FFMPEG=/f/ffmpeg"} {
			if !strings.Contains("\n"+env+"\n", "\n"+kv+"\n") {
				t.Errorf("env lacks %s", kv)
			}
		}
	}
}

func TestPathsFor(t *testing.T) {
	cli := filepath.Join("/h", "runtimes", strconv.Itoa(slidevrt.Major()), "node_modules", "slidev-pptx-export", "bin", "cli.mjs")
	// Node's own layouts, as opening has them: bin/node on Linux, node.exe at the top on Windows.
	for goos, want := range map[string]Paths{
		"linux": {Node: filepath.Join("/h", "node", "bin", "node"), CLI: cli,
			Browser: filepath.Join("/h", "browser-shell", "chrome-headless-shell"), FFmpeg: filepath.Join("/h", "ffmpeg", "ffmpeg")},
		"windows": {Node: filepath.Join("/h", "node", "node.exe"), CLI: cli,
			Browser: filepath.Join("/h", "browser-shell", "chrome-headless-shell.exe"), FFmpeg: filepath.Join("/h", "ffmpeg", "ffmpeg.exe")},
	} {
		if got := PathsFor("/h", goos); got != want {
			t.Errorf("%s paths\n%+v\nwant\n%+v", goos, got, want)
		}
	}
}

func TestRunPassesEventsInOrder(t *testing.T) {
	cmd, out := fakeCmd(t, "events")
	var got []Event
	if err := Run(context.Background(), cmd, out, func(e Event) { got = append(got, e) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Event != "start" || got[0].Slides != 2 ||
		got[1].Event != "clip" || got[1].Slide != 1 || got[1].Click != 2 || got[1].Seconds != 1.5 || got[1].Bytes != 4096 ||
		got[2].Event != "done" || got[2].Path != "x.pptx" || got[2].Clips != 1 || got[2].Bytes != 99 {
		t.Errorf("events %+v", got)
	}
}

func TestRunFailureCarriesTheErrorMessage(t *testing.T) {
	cmd, out := fakeCmd(t, "fail")
	err := Run(context.Background(), cmd, out, func(Event) {})
	if err == nil || !strings.Contains(err.Error(), "Slidev could not start") {
		t.Fatalf("err = %v", err)
	}
	if _, serr := os.Stat(out + ".partial"); serr == nil {
		t.Error(".partial left behind")
	}
}

func TestRunFailureWithoutAnErrorEventCarriesStderr(t *testing.T) {
	cmd, out := fakeCmd(t, "stderr")
	cmd.Env = append(cmd.Env, fakeStderrEnv+"=node: something broke badly")
	var log bytes.Buffer
	cmd.Stderr = &log // as the export session sets it, to its log file
	err := Run(context.Background(), cmd, out, func(Event) {})
	if err == nil || !strings.Contains(err.Error(), "something broke badly") {
		t.Errorf("err = %v, want the exporter's stderr in it", err)
	}
	if !strings.Contains(log.String(), "something broke badly") {
		t.Errorf("the given stderr got %q", log.String())
	}
}

func TestRunCancelStopsTheExporterAndRemovesThePartialFile(t *testing.T) {
	cmd, out := fakeCmd(t, "hang")
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 1)
	errc := make(chan error, 1)
	go func() {
		errc <- Run(ctx, cmd, out, func(e Event) {
			if e.Event == "start" {
				started <- struct{}{}
			}
		})
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("no start event")
	}
	pidText, err := os.ReadFile(out + ".pid")
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(string(pidText))
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if _, serr := os.Stat(out + ".partial"); serr == nil {
		t.Error(".partial left behind")
	}
	deadline := time.Now().Add(5 * time.Second)
	for running(pid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if running(pid) {
		t.Errorf("child %d still runs", pid)
	}
}

// running reports whether pid is a live process (a zombie awaiting reaping is not).
func running(pid int) bool {
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return false
		}
		s := string(b)
		return s[strings.LastIndex(s, ")")+2] != 'Z'
	}
	_, err := os.FindProcess(pid) // on Windows this fails once the process is gone
	return err == nil
}

func TestRunCancelIsNotSuccessEvenIfTheExporterExitsCleanly(t *testing.T) {
	cmd, out := fakeCmd(t, "term")
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 1)
	errc := make(chan error, 1)
	go func() {
		errc <- Run(ctx, cmd, out, func(e Event) {
			if e.Event == "start" {
				started <- struct{}{}
			}
		})
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("no start event")
	}
	time.Sleep(200 * time.Millisecond) // let the helper install its SIGTERM handler
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if _, serr := os.Stat(out + ".partial"); serr == nil {
		t.Error(".partial left behind")
	}
}

func TestRunOverlongLineIsAnErrorAndDoesNotHang(t *testing.T) {
	cmd, out := fakeCmd(t, "longline")
	errc := make(chan error, 1)
	go func() { errc <- Run(context.Background(), cmd, out, func(Event) {}) }()
	select {
	case err := <-errc:
		if err == nil || !strings.Contains(err.Error(), "too long") {
			t.Errorf("err = %v, want an error naming the overlong line", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run hung on an overlong line")
	}
}
