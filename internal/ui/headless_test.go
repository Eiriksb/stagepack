package ui

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"stagepack/internal/save"
)

func TestHeadlessLines(t *testing.T) {
	var buf bytes.Buffer
	h := NewHeadless(&buf, Actions{})
	h.SetStatus("T", save.Saved, "")
	h.Notify("T", "hello")
	h.Error("T", "oops")
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines:\n%s", len(lines), buf.String())
	}
	if !strings.HasPrefix(lines[0], `{"event":"status","status":"saved"`) {
		t.Errorf("status line = %s", lines[0])
	}
	for i, want := range []string{"status", "notify", "error"} {
		var m map[string]string
		if err := json.Unmarshal([]byte(lines[i]), &m); err != nil || m["event"] != want {
			t.Errorf("line %d = %s (%v), want event %s", i, lines[i], err, want)
		}
	}
	if p, ok := h.AskSavePath("/x/y.deck"); !ok || p != "/x/y.deck" {
		t.Errorf("AskSavePath = %q, %v", p, ok)
	}
}

func TestHeadlessRunQuit(t *testing.T) {
	h := NewHeadless(&bytes.Buffer{}, Actions{})
	h.Quit() // before Run: Run returns at once
	done := make(chan struct{})
	readied := false
	go func() { h.Run(func() { readied = true }); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after Quit")
	}
	if !readied {
		t.Error("ready was not called")
	}
	h.Quit() // twice is fine
}

func TestHeadlessOpening(t *testing.T) {
	var buf bytes.Buffer
	h := NewHeadless(&buf, Actions{})
	done := h.Opening("my talk")
	done()
	if got := strings.TrimSpace(buf.String()); got != `{"event":"opening","title":"my talk"}` {
		t.Errorf("lines = %s", got)
	}
}
