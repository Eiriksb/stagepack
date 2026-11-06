package export

import (
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }
func (c *fakeClock) advance(s float64) {
	c.t = c.t.Add(time.Duration(s * float64(time.Second)))
}

func newTestTracker(animations bool) (*Tracker, *fakeClock) {
	c := &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	return NewTracker("talk.sptx", animations, c.now), c
}

func step(t *testing.T, tr *Tracker, want string) float64 {
	t.Helper()
	got, f := tr.Step()
	if got != want {
		t.Fatalf("step = %q, want %q", got, want)
	}
	return f
}

func TestTrackerHead(t *testing.T) {
	a, _ := newTestTracker(false)
	if got := a.Head(); got != "talk.sptx, editable" {
		t.Errorf("head = %q", got)
	}
	b, _ := newTestTracker(true)
	if got := b.Head(); got != "talk.sptx, with animations" {
		t.Errorf("head = %q", got)
	}
}

func TestTrackerSteps(t *testing.T) {
	tr, c := newTestTracker(true)
	step(t, tr, "Unpacking")
	tr.Unpacked()
	if f := step(t, tr, "Starting Slidev"); f != 0 {
		t.Errorf("fraction = %v", f)
	}
	c.advance(10)
	tr.Apply(Event{Event: "start", Slides: 11})
	for n := 1; n <= 3; n++ {
		c.advance(10)
		tr.Apply(Event{Event: "slide", Slide: n, States: 1})
		tr.Apply(Event{Event: "state", Slide: n, State: 0})
	}
	c.advance(10)
	tr.Apply(Event{Event: "slide", Slide: 4, States: 3})
	step(t, tr, "Slide 4 of 11 · about 3 min left")
	tr.Apply(Event{Event: "state", Slide: 4, State: 0})
	tr.Apply(Event{Event: "state", Slide: 4, State: 1})
	c.advance(10) // 60 s in; fraction (3 + 2/3) / 11
	tr.Apply(Event{Event: "record", Slide: 4, Click: 2})
	c.advance(30) // 90 s
	f := step(t, tr, "Slide 4 of 11, recording click 2 · about 3 min left")
	if f < 0.333 || f > 0.334 {
		t.Errorf("fraction = %v", f)
	}
	tr.Apply(Event{Event: "clip", Slide: 4, Click: 2, Seconds: 2, Bytes: 10})
	step(t, tr, "Slide 4 of 11 · about 3 min left")
	tr.Apply(Event{Event: "record", Slide: 4, Click: 3})
	tr.Apply(Event{Event: "state", Slide: 4, State: 2})
	step(t, tr, "Slide 4 of 11 · about 3 min left")
	tr.Apply(Event{Event: "notice", Text: "hello"})
	tr.Apply(Event{Event: "write"})
	text, f := tr.Step()
	if f != 0.97 {
		t.Errorf("fraction after write = %v", f)
	}
	if text != "Writing the file · less than a minute left" {
		t.Errorf("text = %q", text)
	}
	tr.Apply(Event{Event: "done", Path: "x.pptx", Slides: 14})
	if _, f := tr.Step(); f != 1 {
		t.Errorf("fraction after done = %v", f)
	}
}

func TestTrackerTimeLeftWording(t *testing.T) {
	tr, c := newTestTracker(false)
	tr.Apply(Event{Event: "start", Slides: 10})
	tr.Apply(Event{Event: "slide", Slide: 6, States: 1}) // 5/10
	c.advance(40)                                        // 40 s left
	step(t, tr, "Slide 6 of 10 · about 1 min left")
	tr2, c2 := newTestTracker(false)
	tr2.Apply(Event{Event: "start", Slides: 10})
	tr2.Apply(Event{Event: "slide", Slide: 6, States: 1})
	c2.advance(20) // 20 s left
	step(t, tr2, "Slide 6 of 10 · less than a minute left")
}

func TestTrackerNoTimeLeftTooEarly(t *testing.T) {
	tr, c := newTestTracker(false)
	tr.Apply(Event{Event: "start", Slides: 10})
	tr.Apply(Event{Event: "slide", Slide: 5, States: 1}) // 0.4, but only 10 s in
	c.advance(10)
	step(t, tr, "Slide 5 of 10")
	// 20 s in but fraction below 0.15
	tr2, c2 := newTestTracker(false)
	tr2.Apply(Event{Event: "start", Slides: 10})
	tr2.Apply(Event{Event: "slide", Slide: 1, States: 1})
	c2.advance(100)
	step(t, tr2, "Slide 1 of 10")
}

func TestTrackerFractionNeverGoesDown(t *testing.T) {
	tr, _ := newTestTracker(false)
	tr.Apply(Event{Event: "start", Slides: 4})
	tr.Apply(Event{Event: "slide", Slide: 2, States: 2})
	tr.Apply(Event{Event: "state", Slide: 2, State: 0})
	_, hi := tr.Step()
	// A slide that restarts with fewer states, and a slide number going back.
	tr.Apply(Event{Event: "slide", Slide: 2, States: 1})
	_, f := tr.Step()
	if f < hi {
		t.Errorf("fraction went down: %v -> %v", hi, f)
	}
	tr.Apply(Event{Event: "slide", Slide: 1, States: 1})
	if _, f2 := tr.Step(); f2 < f {
		t.Errorf("fraction went down: %v -> %v", f, f2)
	}
	for n := 3; n <= 4; n++ {
		tr.Apply(Event{Event: "slide", Slide: n, States: 1})
		tr.Apply(Event{Event: "state", Slide: n, State: 0})
	}
	if _, f3 := tr.Step(); f3 > 0.95 {
		t.Errorf("fraction before write = %v", f3)
	}
}

func TestTrackerSlideWithNoEvents(t *testing.T) {
	tr, _ := newTestTracker(false)
	tr.Apply(Event{Event: "start", Slides: 10})
	tr.Apply(Event{Event: "slide", Slide: 1, States: 1})
	tr.Apply(Event{Event: "state", Slide: 1, State: 0})
	// slides 2 and 3 failed entirely: no events
	tr.Apply(Event{Event: "slide", Slide: 4, States: 1})
	_, f := tr.Step()
	if f < 0.299 || f > 0.301 { // 3 finished of 10, nothing on slide 4 yet
		t.Errorf("fraction = %v, want 0.3", f)
	}
}

func TestTrackerReannouncedSlide(t *testing.T) {
	tr, _ := newTestTracker(false)
	tr.Apply(Event{Event: "start", Slides: 10})
	tr.Apply(Event{Event: "slide", Slide: 3, States: 4})
	tr.Apply(Event{Event: "state", Slide: 3, State: 0})
	tr.Apply(Event{Event: "state", Slide: 3, State: 1})
	// The click states failed: the slide is announced again with one state,
	// and the state seen again must not be counted twice.
	tr.Apply(Event{Event: "slide", Slide: 3, States: 1})
	tr.Apply(Event{Event: "state", Slide: 3, State: 0})
	_, f := tr.Step()
	if f < 0.299 || f > 0.301 { // 2 finished + a full (clamped) slide 3
		t.Errorf("fraction = %v, want 0.3", f)
	}
	// A slide announcing one state and sending none.
	tr.Apply(Event{Event: "slide", Slide: 4, States: 1})
	if _, f := tr.Step(); f < 0.299 || f > 0.301 {
		t.Errorf("fraction = %v, want 0.3", f)
	}
}
