package save

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"stagepack/internal/brand"
)

func TestJournalKeyUnicodePath(t *testing.T) {
	const id = "3f2a9c1e-0b7d-4c55-9a12-6e8f00c1d2b3"
	a := JournalKey(id, "/x/Åse/Foredrag 1"+brand.Ext)
	b := JournalKey(id, "/x/Ase/Foredrag 1"+brand.Ext)
	if a == b {
		t.Fatalf("keys for different paths are equal: %s", a)
	}
	re := regexp.MustCompile(`^-[0-9a-f]{16}$`)
	for _, k := range []string{a, b} {
		rest, ok := strings.CutPrefix(k, id)
		if !ok || !re.MatchString(rest) {
			t.Errorf("key %q is not the ID followed by -<16 hex>", k)
		}
	}
	if JournalKey(id, "/x/a"+brand.Ext) != JournalKey(id, "/x/a"+brand.Ext) {
		t.Error("key is not stable")
	}
}

func TestJournalKeyUnsafeID(t *testing.T) {
	k := JournalKey("../../etc", "/x/a"+brand.Ext)
	if !regexp.MustCompile(`^[0-9a-f-]+$`).MatchString(k) {
		t.Errorf("key %q from an unsafe ID is not plain hex", k)
	}
}

func TestJournalPutGetClear(t *testing.T) {
	j := Journal{Dir: filepath.Join(t.TempDir(), "journal", "nested")} // created on demand
	key := JournalKey("id1", "/x/a"+brand.Ext)
	if _, ok, err := j.Get(key); ok || err != nil {
		t.Fatalf("Get on empty journal = %v, %v", ok, err)
	}
	want := Entry{
		DeckPath:    "/x/a" + brand.Ext,
		DeckID:      "id1",
		BaseUpdated: time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC),
		Saved:       time.Date(2026, 10, 22, 9, 5, 0, 0, time.UTC),
		Slides:      []byte("# notes\n\x00binary-safe"),
	}
	if err := j.Put(key, want); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(j.Dir, key+".json")); err != nil {
		t.Fatalf("entry not stored as <key>.json: %v", err)
	}
	got, ok, err := j.Get(key)
	if err != nil || !ok {
		t.Fatalf("Get = %v, %v", ok, err)
	}
	if got.DeckPath != want.DeckPath || got.DeckID != want.DeckID || !got.BaseUpdated.Equal(want.BaseUpdated) ||
		!got.Saved.Equal(want.Saved) || string(got.Slides) != string(want.Slides) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if err := j.Clear(key); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := j.Get(key); ok {
		t.Error("entry still there after Clear")
	}
	if err := j.Clear(key); err != nil {
		t.Errorf("Clear of a missing entry = %v, want nil", err)
	}
	left, _ := os.ReadDir(j.Dir)
	if len(left) != 0 {
		t.Errorf("journal dir not empty: %v", left)
	}
}

func TestJournalRejectsPathKeys(t *testing.T) {
	j := Journal{Dir: t.TempDir()}
	for _, key := range []string{"", "../x", "a/b", `a\b`, ".."} {
		if err := j.Put(key, Entry{}); err == nil {
			t.Errorf("Put(%q) = nil, want an error", key)
		}
	}
}

func TestRecoverApplies(t *testing.T) {
	deckPath, m := packFixture(t)
	j := Journal{Dir: filepath.Join(t.TempDir(), "journal")}
	key := JournalKey(m.ID, deckPath)
	if err := j.Put(key, Entry{DeckPath: deckPath, DeckID: m.ID, BaseUpdated: m.Updated, Saved: m.Updated.Add(time.Minute), Slides: []byte("# recovered\n")}); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(t.TempDir(), "slides.md")
	writeFile(t, work, "# old\n")

	applied, kept, err := Recover(j, key, m, work)
	if err != nil {
		t.Fatal(err)
	}
	if !applied || kept != "" {
		t.Fatalf("applied=%v kept=%q, want true, \"\"", applied, kept)
	}
	if got := string(readFile(t, work)); got != "# recovered\n" {
		t.Errorf("work slides = %q", got)
	}
	// The entry stays until the deck file has it; the watcher writes it back.
	if _, ok, _ := j.Get(key); !ok {
		t.Error("journal entry gone before it is in the deck file")
	}
}

func TestRecoverNothing(t *testing.T) {
	_, m := packFixture(t)
	j := Journal{Dir: t.TempDir()}
	work := filepath.Join(t.TempDir(), "slides.md")
	writeFile(t, work, "# old\n")
	applied, kept, err := Recover(j, JournalKey(m.ID, "/nowhere"), m, work)
	if applied || kept != "" || err != nil {
		t.Fatalf("Recover with no entry = %v, %q, %v", applied, kept, err)
	}
}

func TestRecoverSkipsWhenDeckChangedSinceEdit(t *testing.T) {
	deckPath, m := packFixture(t)
	j := Journal{Dir: filepath.Join(t.TempDir(), "journal")}
	key := JournalKey(m.ID, deckPath)
	if err := j.Put(key, Entry{DeckPath: deckPath, DeckID: m.ID, BaseUpdated: m.Updated, Slides: []byte("# stale\n")}); err != nil {
		t.Fatal(err)
	}
	newer := m
	newer.Updated = m.Updated.Add(time.Hour) // someone saved the deck elsewhere since
	work := filepath.Join(t.TempDir(), "slides.md")
	writeFile(t, work, "# current\n")

	applied, kept, err := Recover(j, key, newer, work)
	if err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Fatal("applied = true for a deck changed since the edit")
	}
	if kept == "" {
		t.Fatal("kept is empty")
	}
	data, err := os.ReadFile(kept)
	if err != nil {
		t.Fatalf("kept file %q: %v", kept, err)
	}
	if !strings.Contains(string(data), "IyBzdGFsZQo=") { // base64 of "# stale\n"
		t.Errorf("kept file does not hold the journaled slides: %s", data)
	}
	if got := string(readFile(t, work)); got != "# current\n" {
		t.Errorf("work slides changed to %q", got)
	}
	// The kept file is out of the way: a new edit under the same key must not overwrite it.
	if _, ok, _ := j.Get(key); ok {
		t.Error("the stale entry is still under the deck's key")
	}
}

func TestSetAside(t *testing.T) {
	j := Journal{Dir: t.TempDir()}
	key := JournalKey("id", "/a")
	if kept, err := SetAside(j, key); kept != "" || err != nil {
		t.Fatalf("SetAside with no entry = %q, %v", kept, err)
	}
	writeFile(t, filepath.Join(j.Dir, key+".json"), "{not json")
	kept, err := SetAside(j, key)
	if err != nil || kept == "" {
		t.Fatalf("SetAside = %q, %v", kept, err)
	}
	if got := string(readFile(t, kept)); got != "{not json" {
		t.Errorf("kept file = %q", got)
	}
	if _, err := os.Stat(filepath.Join(j.Dir, key+".json")); !os.IsNotExist(err) {
		t.Errorf("entry still under its key (%v)", err)
	}
}
