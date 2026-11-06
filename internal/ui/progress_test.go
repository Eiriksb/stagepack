package ui

import (
	"bytes"
	"testing"
	"time"
)

func TestHeadlessProgressLines(t *testing.T) {
	var buf bytes.Buffer
	p := NewHeadlessProgress(&buf)
	p.Update("Unpacking", 0)
	p.Update("Slide 4 of 11", 0.42)
	p.Close()
	p.Update("after Close", 0.5) // writes nothing
	p.Close()                    // twice is fine
	if got := HeadlessFinished(&buf, "Exported talk.pptx", "11 slides, 3 with a video, 2.1 MB",
		[]string{"Open", "Show in folder", "Close"}); got != -1 {
		t.Errorf("HeadlessFinished = %d, want -1", got)
	}
	want := `{"event":"progress","step":"Unpacking","fraction":0}
{"event":"progress","step":"Slide 4 of 11","fraction":0.42}
{"event":"finished","title":"Exported talk.pptx","message":"11 slides, 3 with a video, 2.1 MB","buttons":["Open","Show in folder","Close"]}
`
	if got := buf.String(); got != want {
		t.Errorf("lines:\n%s\nwant:\n%s", got, want)
	}
	select {
	case <-p.Cancelled():
		t.Error("headless progress was cancelled")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestPercent(t *testing.T) {
	for f, want := range map[float64]int{0: 0, 0.29: 29, 0.42: 42, 0.999: 100, 1: 100, -1: 0, 7: 100} {
		if got := percent(f); got != want {
			t.Errorf("percent(%v) = %d, want %d", f, got, want)
		}
	}
	nan := 0.0
	if got := percent(nan / nan); got != 0 {
		t.Errorf("percent(NaN) = %d", got)
	}
}

func TestPlainFinishedAnswer(t *testing.T) {
	// A box with only OK, where a finished box can't be shown: OK is Close.
	for _, c := range []struct {
		buttons []string
		want    int
	}{
		{[]string{"Open", "Show in folder", "Close"}, 2},
		{[]string{"Close", "Open"}, 0},
		{[]string{"Open"}, -1},
		{nil, -1},
	} {
		if got := plainFinishedAnswer(c.buttons); got != c.want {
			t.Errorf("plainFinishedAnswer(%q) = %d, want %d", c.buttons, got, c.want)
		}
	}
}
