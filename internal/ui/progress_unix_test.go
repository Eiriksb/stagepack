//go:build !windows

package ui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeBin makes the given shell scripts the only programs on PATH, so the real
// lookPath and command starters run them (and never a real dialog). Each script
// is a /bin/sh body; $DIR is the folder they, and what they record, live in.
// A display is set, as on a desktop.
func fakeBin(t *testing.T, scripts map[string]string) (dir string) {
	t.Helper()
	dir = t.TempDir()
	for name, body := range scripts {
		s := "#!/bin/sh\nDIR='" + dir + "'\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	t.Setenv("DISPLAY", ":99")
	t.Setenv("WAYLAND_DISPLAY", "")
	return dir
}

// recordStdin is a fake's body that records its input to $DIR/stdin. The fakes
// use only the shell's builtins, as PATH holds nothing else.
const recordStdin = `while IFS= read -r line; do printf '%s\n' "$line" >> "$DIR/stdin"; done`

// script is a fake that records its arguments, one per line, to $DIR/<name>.args.
func script(name, rest string) string {
	return `for a in "$@"; do printf '%s\n' "$a" >> "$DIR/` + name + `.args"; done` + "\n" + rest
}

func read(dir, name string) string {
	b, _ := os.ReadFile(filepath.Join(dir, name))
	return string(b)
}

// waitFor waits up to 5 s for ok.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func fired(c <-chan struct{}, wait time.Duration) bool {
	select {
	case <-c:
		return true
	case <-time.After(wait):
		return false
	}
}

func TestZenityProgressArgs(t *testing.T) {
	dir := fakeBin(t, map[string]string{"zenity": script("zenity", recordStdin)})
	p := ShowProgress("Stagepack", `talk <1> & C:\x.sptx, with animations`)
	defer p.Close()
	waitFor(t, "zenity's arguments", func() bool { return strings.HasSuffix(read(dir, "zenity.args"), "--width=420\n") })
	got := strings.Split(strings.TrimSuffix(read(dir, "zenity.args"), "\n"), "\n")
	// The progress box has no --no-markup: the text is escaped instead.
	want := []string{"--progress", "--title=Stagepack", `--text=talk &lt;1&gt; &amp; C:\\x.sptx, with animations`,
		"--percentage=0", "--width=420"}
	if !slices.Equal(got, want) {
		t.Errorf("args %q, want %q", got, want)
	}
}

func TestZenityProgressInput(t *testing.T) {
	dir := fakeBin(t, map[string]string{"zenity": script("zenity", recordStdin)})
	p := ShowProgress("Stagepack", "")
	p.Update("Slide 4 of 11", 0.42)
	waitFor(t, "the step", func() bool { return read(dir, "stdin") != "" })
	if got := read(dir, "stdin"); got != "# Slide 4 of 11\n42\n" {
		t.Errorf("stdin %q", got)
	}
	start := time.Now()
	p.Close()
	if d := time.Since(start); d > time.Second {
		t.Errorf("Close took %v: zenity stays open after its input ends, so Close must end it", d)
	}
	if fired(p.Cancelled(), 50*time.Millisecond) {
		t.Error("Close counted as Cancel")
	}

	// With a head line, the step is its second line: zenity reads \n in a # line as a line break.
	dir = fakeBin(t, map[string]string{"zenity": script("zenity", recordStdin)})
	p = ShowProgress("Stagepack", `talk.sptx, with animations`)
	defer p.Close()
	p.Update(`Slide 1 of 2 C:\x`, 0.29)
	waitFor(t, "the step", func() bool { return strings.Count(read(dir, "stdin"), "\n") >= 2 })
	if got := read(dir, "stdin"); got != `# talk.sptx, with animations\nSlide 1 of 2 C:\\x`+"\n29\n" {
		t.Errorf("stdin %q", got)
	}
}

func TestZenityExitMeansCancelled(t *testing.T) {
	// Cancel, or closing the window: zenity ends with 1.
	fakeBin(t, map[string]string{"zenity": "IFS= read -r line; exit 1"})
	p := ShowProgress("Stagepack", "talk.sptx, editable")
	p.Update("Unpacking", 0)
	if !fired(p.Cancelled(), 5*time.Second) {
		t.Fatal("zenity ended but Cancelled did not fire")
	}
	p.Update("Slide 1 of 2", 0.1) // no box: nothing happens
	p.Close()

	// A box that could not be shown (an error, or killed) is not the person cancelling.
	fakeBin(t, map[string]string{"zenity": "exit 255"})
	p = ShowProgress("Stagepack", "talk.sptx, editable")
	if fired(p.Cancelled(), 300*time.Millisecond) {
		t.Error("zenity failing counted as Cancel")
	}
	p.Close()
}

func TestZenityFinishedReturnsTheButton(t *testing.T) {
	buttons := []string{"Open", "Show in folder", "Close"}
	for _, c := range []struct {
		body string
		want int
	}{
		{`printf 'Show in folder\n'; exit 1`, 1}, // an extra button: zenity prints its label
		{`printf 'Open\n'; exit 1`, 0},
		{`exit 0`, 2},  // the OK button, which is the last
		{`exit 1`, -1}, // the window closed
	} {
		dir := fakeBin(t, map[string]string{"zenity": script("zenity", c.body)})
		if got := Finished("Exported talk.pptx", `11 slides, 3 with a video, 2.1 MB <b> C:\x`, buttons); got != c.want {
			t.Errorf("%s: Finished = %d, want %d", c.body, got, c.want)
		}
		args := strings.Split(read(dir, "zenity.args"), "\n")
		for _, want := range []string{"--info", "--title=Exported talk.pptx", "--no-markup", "--ok-label=Close",
			"--extra-button=Open", "--extra-button=Show in folder", `--text=11 slides, 3 with a video, 2.1 MB <b> C:\\x`} {
			if !slices.Contains(args, want) {
				t.Errorf("args %q lack %q", args, want)
			}
		}
	}
}

func TestKdialogFinished(t *testing.T) {
	buttons := []string{"Open", "Show in folder", "Close"}
	for code, want := range map[string]int{"0": 0, "1": 1, "2": 2} {
		dir := fakeBin(t, map[string]string{"kdialog": script("kdialog", "exit "+code)})
		if got := Finished("Exported talk.pptx", "11 slides", buttons); got != want {
			t.Errorf("kdialog exit %s: Finished = %d, want %d", code, got, want)
		}
		want := "--title\nExported talk.pptx\n--yes-label\nOpen\n--no-label\nShow in folder\n--cancel-label\nClose\n--yesnocancel\n11 slides\n"
		if got := read(dir, "kdialog.args"); got != want {
			t.Errorf("kdialog args %q", got)
		}
	}
}

func TestFinishedWithoutDialogsNotifies(t *testing.T) {
	dir := fakeBin(t, map[string]string{"notify-send": script("notify-send", "")})
	if got := Finished("Exported talk.pptx", "11 slides", []string{"Open", "Close"}); got != -1 {
		t.Errorf("Finished = %d, want -1", got)
	}
	if got := read(dir, "notify-send.args"); !strings.Contains(got, "Exported talk.pptx\n11 slides\n") {
		t.Errorf("notify-send args %q", got)
	}
}

// fakeKdialog prints the D-Bus address of its progress box, as kdialog does.
const fakeKdialog = `printf 'org.kde.kdialog-4242 /ProgressDialog\n'`

func TestKdialogProgressCalls(t *testing.T) {
	old := kdialogPoll
	kdialogPoll = 10 * time.Millisecond
	t.Cleanup(func() { kdialogPoll = old })

	// Each D-Bus call is one line of arguments joined by |; wasCancelled says
	// true once $DIR/cancel exists.
	const qdbus = `(IFS='|'; printf '%s\n' "$*") >> "$DIR/calls"
if [ "$3" = wasCancelled ]; then if [ -e "$DIR/cancel" ]; then echo true; else echo false; fi; fi`
	dir := fakeBin(t, map[string]string{"kdialog": script("kdialog", fakeKdialog), "qdbus": qdbus})
	p := ShowProgress("Stagepack", "talk.sptx, with animations")
	if got := read(dir, "kdialog.args"); got != "--title\nStagepack\n--progressbar\ntalk.sptx, with animations\n100\n" {
		t.Errorf("kdialog args %q", got)
	}
	p.Update("Slide 4 of 11", 0.42)
	const ref = "org.kde.kdialog-4242|/ProgressDialog|"
	waitFor(t, "the step", func() bool { return strings.Contains(read(dir, "calls"), "setLabelText") })
	waitFor(t, "a cancel check after it", func() bool {
		c := read(dir, "calls")
		return strings.Contains(c[strings.Index(c, "setLabelText"):], "wasCancelled")
	})
	p.Close()
	calls := read(dir, "calls")
	for _, want := range []string{
		ref + "showCancelButton|true\n",
		ref + "Set||value|42\n",
		ref + "setLabelText|talk.sptx, with animations\nSlide 4 of 11\n",
		ref + "wasCancelled\n",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("calls lack %q:\n%s", want, calls)
		}
	}
	if !strings.HasPrefix(calls, ref+"showCancelButton|true\n") || !strings.HasSuffix(calls, ref+"close\n") {
		t.Errorf("calls should start with showCancelButton and end with close:\n%s", calls)
	}
	if fired(p.Cancelled(), 30*time.Millisecond) {
		t.Error("Close counted as Cancel")
	}
	after := read(dir, "calls")
	time.Sleep(50 * time.Millisecond)
	if read(dir, "calls") != after {
		t.Error("D-Bus calls went on after Close")
	}

	// Cancel in the box.
	dir = fakeBin(t, map[string]string{"kdialog": script("kdialog", fakeKdialog), "qdbus": qdbus})
	p = ShowProgress("Stagepack", "talk.sptx, editable")
	os.WriteFile(filepath.Join(dir, "cancel"), nil, 0o644)
	if !fired(p.Cancelled(), 5*time.Second) {
		t.Error("Cancel in kdialog's box did not fire Cancelled")
	}
	p.Close()
	if c := read(dir, "calls"); !strings.HasSuffix(c, ref+"close\n") {
		t.Errorf("the box was not closed after Cancel:\n%s", c)
	}
}

func TestKdialogProgressOverGdbus(t *testing.T) {
	old := kdialogPoll
	kdialogPoll = 10 * time.Millisecond
	t.Cleanup(func() { kdialogPoll = old })
	const gdbus = `(IFS='|'; printf '%s\n' "$*") >> "$DIR/calls"
case "$*" in *wasCancelled*) echo '(false,)';; esac`
	// qdbus is there but can't reach the bus (as when it can't find its Qt): gdbus is used.
	dir := fakeBin(t, map[string]string{"kdialog": script("kdialog", fakeKdialog), "gdbus": gdbus, "qdbus": "exit 1"})
	p := ShowProgress("Stagepack", `talk "1".sptx`)
	p.Update("Slide 4 of 11", 0.42)
	waitFor(t, "the step", func() bool { return strings.Contains(read(dir, "calls"), "setLabelText") })
	p.Close()
	const call = "call|--session|--dest|org.kde.kdialog-4242|--object-path|/ProgressDialog|--method|"
	calls := read(dir, "calls")
	for _, want := range []string{
		call + "org.kde.kdialog.ProgressDialog.showCancelButton|true\n",
		call + `org.freedesktop.DBus.Properties.Set|"org.kde.kdialog.ProgressDialog"|"value"|<int32 42>` + "\n",
		call + `org.kde.kdialog.ProgressDialog.setLabelText|"talk \"1\".sptx\nSlide 4 of 11"` + "\n",
		call + "org.kde.kdialog.ProgressDialog.close\n",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("calls lack %q:\n%s", want, calls)
		}
	}
	if fired(p.Cancelled(), 30*time.Millisecond) {
		t.Error("(false,) counted as Cancel")
	}
}

func TestNoDialogProgramNotifiesAndNeverCancels(t *testing.T) {
	for _, c := range []struct {
		name    string
		scripts map[string]string
		display bool
	}{
		{"no dialog program", map[string]string{}, true},
		// zenity can't show a box without a display, and would end with 1, as for Cancel.
		{"no display", map[string]string{"zenity": script("zenity", "exit 1")}, false},
	} {
		c.scripts["notify-send"] = script("notify-send", "")
		dir := fakeBin(t, c.scripts)
		if !c.display {
			t.Setenv("DISPLAY", "")
		}
		p := ShowProgress("Stagepack", "talk.sptx, with animations")
		if got := read(dir, "notify-send.args"); !strings.Contains(got, "Stagepack\ntalk.sptx, with animations\n") {
			t.Errorf("%s: notify-send args %q", c.name, got)
		}
		if read(dir, "zenity.args") != "" {
			t.Errorf("%s: zenity ran", c.name)
		}
		p.Update("Slide 1 of 2", 0.1)
		p.Update("Slide 2 of 2", 0.6)
		if fired(p.Cancelled(), 50*time.Millisecond) {
			t.Errorf("%s: Cancelled fired", c.name)
		}
		p.Close()
		p.Close()
	}
}
