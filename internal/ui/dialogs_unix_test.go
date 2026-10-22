//go:build !windows

package ui

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// fakeTools makes the named programs the only ones installed; run answers each dialog.
type fakeTools struct {
	installed []string
	run       func(name string, args []string) (string, int, error)
	calls     []string // "name arg arg…"
}

func withTools(t *testing.T, f *fakeTools) {
	t.Helper()
	oldLook, oldRun, oldStart := lookPath, runDialog, startDialog
	t.Cleanup(func() { lookPath, runDialog, startDialog = oldLook, oldRun, oldStart })
	lookPath = func(name string) (string, error) {
		if slices.Contains(f.installed, name) {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
	runDialog = func(name string, args []string) (string, int, error) {
		f.calls = append(f.calls, name+" "+strings.Join(args, " "))
		if f.run == nil {
			return "", 0, nil
		}
		return f.run(name, args)
	}
	startDialog = func(name string, args []string) (func(), error) {
		f.calls = append(f.calls, "start "+name+" "+strings.Join(args, " "))
		return func() { f.calls = append(f.calls, "stop "+name) }, nil
	}
}

func TestZenityQuestionDefaultsToNo(t *testing.T) {
	a := dialogArgs("zenity", dialog{kind: kindQuestion, title: "T", msg: `run C:\x <b>&`})
	for _, want := range []string{"--question", "--default-cancel", "--no-markup", "--ok-label=Yes", "--cancel-label=No", "--title=T", `--text=run C:\\x <b>&`} {
		if !slices.Contains(a, want) {
			t.Errorf("args %q lack %q", a, want)
		}
	}
	// The progress dialog has no --no-markup: the text is escaped instead.
	b := dialogArgs("zenity", dialog{kind: kindBusy, title: "T", msg: "Opening <a & b>…"})
	if !slices.Contains(b, "--text=Opening &lt;a &amp; b&gt;…") {
		t.Errorf("busy args %q", b)
	}
	for _, k := range []dialogKind{kindInfo, kindError, kindQuestion, kindSave, kindBusy} {
		for _, tool := range dialogTools {
			if dialogArgs(tool, dialog{kind: k, title: "T", msg: "m", suggest: "/a/b.sptx", filterName: "F", ext: ".sptx"}) == nil {
				t.Errorf("%s has no command line for kind %d", tool, k)
			}
		}
	}
}

func TestConfirmAnswers(t *testing.T) {
	for _, c := range []struct {
		installed []string
		codes     map[string]int
		want      bool
		used      []string
	}{
		{[]string{"zenity", "kdialog"}, map[string]int{"zenity": 0}, true, []string{"zenity"}},
		{[]string{"zenity", "kdialog"}, map[string]int{"zenity": 1}, false, []string{"zenity"}},
		// zenity can't show it (no display): kdialog is asked.
		{[]string{"zenity", "kdialog"}, map[string]int{"zenity": 255, "kdialog": 0}, true, []string{"zenity", "kdialog"}},
		{[]string{"kdialog"}, map[string]int{"kdialog": 0}, true, []string{"kdialog"}},
		// Nobody to ask: no, and a notification says so.
		{[]string{"notify-send"}, nil, false, []string{"notify-send"}},
		{nil, nil, false, nil},
	} {
		f := &fakeTools{installed: c.installed, run: func(name string, _ []string) (string, int, error) { return "", c.codes[name], nil }}
		withTools(t, f)
		if got := Confirm("T", "run it?"); got != c.want {
			t.Errorf("%v %v: Confirm = %v, want %v", c.installed, c.codes, got, c.want)
		}
		var used []string
		for _, call := range f.calls {
			used = append(used, strings.Fields(call)[0])
		}
		if !slices.Equal(used, c.used) {
			t.Errorf("%v %v: ran %q, want %q", c.installed, c.codes, used, c.used)
		}
	}
}

func TestMessagesWithoutDialogsGoToNotifications(t *testing.T) {
	f := &fakeTools{installed: []string{"notify-send"}}
	withTools(t, f)
	Alert("Stagepack", "it broke")
	if len(f.calls) != 1 || f.calls[0] != "notify-send --app-name=Stagepack -- Stagepack it broke" {
		t.Errorf("calls = %q", f.calls)
	}
}

func TestSaveBox(t *testing.T) {
	f := &fakeTools{installed: []string{"zenity"}, run: func(string, []string) (string, int, error) { return "/home/u/copy.sptx\n", 0, nil }}
	withTools(t, f)
	p, ok, err := saveBox("Save a copy", "/home/u/talk.sptx", "Stagepack talk", ".sptx")
	if p != "/home/u/copy.sptx" || !ok || err != nil {
		t.Errorf("saveBox = %q, %v, %v", p, ok, err)
	}
	if !strings.Contains(f.calls[0], "--filename=/home/u/talk.sptx") || !strings.Contains(f.calls[0], "--file-filter=Stagepack talk | *.sptx") {
		t.Errorf("call %q", f.calls[0])
	}
	f.run = func(string, []string) (string, int, error) { return "", 1, nil }
	if _, ok, err := saveBox("S", "/x.sptx", "F", ".sptx"); ok || err != nil {
		t.Errorf("cancelled: ok %v, err %v", ok, err)
	}
	f.installed = nil
	if _, ok, err := saveBox("S", "/x.sptx", "F", ".sptx"); ok || err == nil {
		t.Errorf("no dialog program: ok %v, err %v", ok, err)
	}
}

func TestBusy(t *testing.T) {
	f := &fakeTools{installed: []string{"zenity", "notify-send"}}
	withTools(t, f)
	done := Busy("Stagepack", "Opening talk…")
	done()
	if len(f.calls) != 2 || !strings.HasPrefix(f.calls[0], "start zenity ") || !strings.Contains(f.calls[0], "--progress") || f.calls[1] != "stop zenity" {
		t.Errorf("calls = %q", f.calls)
	}
	f.calls, f.installed = nil, []string{"notify-send"}
	Busy("Stagepack", "Opening talk…")()
	if len(f.calls) != 1 || !strings.HasPrefix(f.calls[0], "notify-send ") {
		t.Errorf("without zenity: calls = %q", f.calls)
	}
}
