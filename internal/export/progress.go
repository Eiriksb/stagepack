package export

import (
	"fmt"
	"math"
	"time"
)

// Tracker turns the exporter's events into the progress box's head line, step
// text and bar fraction.
type Tracker struct {
	file       string
	animations bool
	now        func() time.Time
	began      time.Time

	unpacked  bool
	slides    int // Slidev's slide count, from start
	slide     int // the latest slide number seen
	states    int // states announced for the latest slide
	seen      map[int]map[int]bool
	recording int // click being recorded, 0 when none
	writing   bool
	done      bool
	fraction  float64
}

// NewTracker starts timing from now. The text begins as "Unpacking".
func NewTracker(file string, animations bool, now func() time.Time) *Tracker {
	return &Tracker{file: file, animations: animations, now: now, began: now(), seen: map[int]map[int]bool{}}
}

// Head is the box's first line.
func (t *Tracker) Head() string {
	if t.animations {
		return t.file + ", with animations"
	}
	return t.file + ", editable"
}

// Unpacked tells the tracker the exporter is unpacked and being started.
func (t *Tracker) Unpacked() { t.unpacked = true }

// Apply takes in one event.
func (t *Tracker) Apply(e Event) {
	switch e.Event {
	case "start":
		t.unpacked = true
		t.slides = e.Slides
	case "slide":
		t.recording = 0
		if e.Slide >= t.slide {
			t.slide = e.Slide
			t.states = e.States
		}
	case "state":
		t.recording = 0
		if e.Slide > t.slide {
			t.slide = e.Slide
			t.states = 0
		}
		if t.seen[e.Slide] == nil {
			t.seen[e.Slide] = map[int]bool{}
		}
		t.seen[e.Slide][e.State] = true
	case "record":
		t.recording = e.Click
	case "clip":
		t.recording = 0
	case "write":
		t.recording = 0
		t.writing = true
	case "done":
		t.recording = 0
		t.done = true
	}
	t.update()
}

func (t *Tracker) update() {
	f := t.compute()
	if f > t.fraction {
		t.fraction = f
	}
}

func (t *Tracker) compute() float64 {
	if t.done {
		return 1
	}
	if t.writing {
		return 0.97
	}
	if t.slides <= 0 || t.slide <= 0 {
		return 0
	}
	share := 0.0
	if t.states > 0 {
		share = float64(len(t.seen[t.slide])) / float64(t.states)
	}
	share = math.Max(0, math.Min(1, share))
	return math.Min(0.95, (float64(t.slide-1)+share)/float64(t.slides))
}

// Step is the box's step text and the bar's fraction, 0 to 1.
func (t *Tracker) Step() (string, float64) {
	var text string
	switch {
	case t.done, t.writing:
		text = "Writing the file"
	case t.slide > 0 && t.recording > 0:
		text = fmt.Sprintf("Slide %d of %d, recording click %d", t.slide, t.slides, t.recording)
	case t.slide > 0:
		text = fmt.Sprintf("Slide %d of %d", t.slide, t.slides)
	case t.unpacked:
		text = "Starting Slidev"
	default:
		text = "Unpacking"
	}
	if !t.done {
		if left := t.timeLeft(); left != "" {
			text += " · " + left
		}
	}
	return text, t.fraction
}

func (t *Tracker) timeLeft() string {
	elapsed := t.now().Sub(t.began).Seconds()
	if t.fraction < 0.15 || elapsed < 20 {
		return ""
	}
	secs := elapsed * (1 - t.fraction) / t.fraction
	if secs < 30 {
		return "less than a minute left"
	}
	return fmt.Sprintf("about %d min left", int(math.Ceil(secs/60-1e-6)))
}
