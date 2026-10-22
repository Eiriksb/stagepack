package save

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"stagepack/internal/brand"
)

type entryInfo struct {
	Name           string
	Method         uint16
	CRC32          uint32
	CompressedSize uint64
}

func entryInfos(t *testing.T, deckPath string) []entryInfo {
	t.Helper()
	zr, err := zip.OpenReader(deckPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var out []entryInfo
	for _, f := range zr.File {
		out = append(out, entryInfo{f.Name, f.Method, f.CRC32, f.CompressedSize64})
	}
	return out
}

func TestRewriteEntryReplacesOnlyThat(t *testing.T) {
	deckPath, m := packFixture(t)
	before := entryInfos(t, deckPath)
	now := time.Date(2026, 10, 22, 12, 34, 56, 789, time.UTC)
	newSlides := "# Edited\n\n<!-- new note -->\n"

	if err := RewriteEntry(deckPath, "slides.md", []byte(newSlides), now); err != nil {
		t.Fatal(err)
	}

	if got := entryText(t, deckPath, "slides.md"); got != newSlides {
		t.Errorf("slides.md = %q, want %q", got, newSlides)
	}
	after := entryInfos(t, deckPath)
	if len(after) != len(before) {
		t.Fatalf("%d entries after, %d before", len(after), len(before))
	}
	sawClip := false
	for i := range before {
		b, a := before[i], after[i]
		if a.Name != b.Name {
			t.Errorf("entry %d is %s, was %s (order changed)", i, a.Name, b.Name)
			continue
		}
		switch a.Name {
		case "slides.md", brand.ManifestName:
			if a.Method != zip.Deflate {
				t.Errorf("%s method %d, want Deflate", a.Name, a.Method)
			}
		default:
			if a != b {
				t.Errorf("%s changed: %+v -> %+v", a.Name, b, a)
			}
		}
		if a.Name == "public/clip.mp4" {
			sawClip = true
			if a.Method != zip.Store {
				t.Errorf("public/clip.mp4 method %d, want Store", a.Method)
			}
		}
	}
	if !sawClip {
		t.Error("fixture has no public/clip.mp4")
	}

	got := manifestOf(t, deckPath)
	if !got.Updated.Equal(now) {
		t.Errorf("updated = %v, want %v", got.Updated, now)
	}
	want := m
	want.Updated = got.Updated
	if !reflect.DeepEqual(normal(got), normal(want)) {
		t.Errorf("manifest = %+v, want %+v", got, want)
	}
	noTmp(t, deckPath)
}

// noTmp fails if a temp file is left next to the deck.
func noTmp(t *testing.T, deckPath string) {
	t.Helper()
	left, err := filepath.Glob(filepath.Join(filepath.Dir(deckPath), ".*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

// Two writers on one deck (two viewers, say) each rename only their own temp
// file, so the deck is always one whole zip.
func TestRewriteEntryConcurrentWriters(t *testing.T) {
	deckPath, m := packFixture(t)
	payloads := []string{"# writer A\n" + strings.Repeat("a", 1<<16), "# writer B\n" + strings.Repeat("b", 1<<16)}
	for round := 0; round < 20; round++ {
		var wg sync.WaitGroup
		errs := make([]error, len(payloads))
		for i, p := range payloads {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = RewriteEntry(deckPath, "slides.md", []byte(p), time.Now())
			}()
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d, writer %d: %v", round, i, err)
			}
		}
		if got := manifestOf(t, deckPath); got.ID != m.ID {
			t.Fatalf("round %d: manifest id %q, want %q", round, got.ID, m.ID)
		}
		if got := entryText(t, deckPath, "slides.md"); got != payloads[0] && got != payloads[1] {
			t.Fatalf("round %d: slides.md is neither payload (%d bytes)", round, len(got))
		}
		noTmp(t, deckPath)
	}
}

// normal makes times comparable with DeepEqual.
func normal(m any) string {
	b, _ := json.Marshal(m)
	return string(b)
}

func TestRewriteEntryKeepsUnknownManifestFields(t *testing.T) {
	deckPath, _ := packFixture(t)
	// Put an extra manifest field in, as a later viewer of the same format might.
	raw := entryText(t, deckPath, brand.ManifestName)
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatal(err)
	}
	obj["future"] = map[string]any{"x": 1.0}
	extra, _ := json.Marshal(obj)
	if err := rewriteEntries(deckPath, map[string][]byte{brand.ManifestName: extra}); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 10, 22, 13, 0, 0, 0, time.UTC)
	if err := RewriteEntry(deckPath, "slides.md", []byte("# x\n"), now); err != nil {
		t.Fatal(err)
	}
	var after map[string]any
	if err := json.Unmarshal([]byte(entryText(t, deckPath, brand.ManifestName)), &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after["future"], obj["future"]) {
		t.Errorf("unknown field lost: %v", after["future"])
	}
	if after["title"] != obj["title"] || after["id"] != obj["id"] {
		t.Errorf("known fields changed: %v", after)
	}
}

// rewriteEntries replaces entries' bytes in place (test helper, no manifest changes).
func rewriteEntries(deckPath string, repl map[string][]byte) error {
	zr, err := zip.OpenReader(deckPath)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		if data, ok := repl[f.Name]; ok {
			w, err := zw.CreateHeader(&zip.FileHeader{Name: f.Name, Method: zip.Deflate})
			if err != nil {
				return err
			}
			w.Write(data)
			continue
		}
		if err := zw.Copy(f); err != nil {
			return err
		}
	}
	zr.Close()
	if err := zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(deckPath, buf.Bytes(), 0o644)
}

type failingWriter struct {
	w    io.Writer
	left int
}

func (f *failingWriter) Write(p []byte) (int, error) {
	if len(p) > f.left {
		n, _ := f.w.Write(p[:f.left])
		f.left = 0
		return n, errors.New("disk fell off")
	}
	f.left -= len(p)
	return f.w.Write(p)
}

func TestRewriteEntryCrashLeavesOriginal(t *testing.T) {
	deckPath, _ := packFixture(t)
	orig := readFile(t, deckPath)
	tmpWriter = func(w io.Writer) io.Writer { return &failingWriter{w: w, left: len(orig) / 2} }
	t.Cleanup(func() { tmpWriter = nil })

	err := RewriteEntry(deckPath, "slides.md", []byte("# lost\n"), time.Now())
	if err == nil {
		t.Fatal("RewriteEntry succeeded through a failing writer")
	}
	if !bytes.Equal(readFile(t, deckPath), orig) {
		t.Error("original deck changed")
	}
	noTmp(t, deckPath)
	left, _ := os.ReadDir(filepath.Dir(deckPath))
	if len(left) != 1 {
		t.Errorf("deck folder holds %v, want only the deck", left)
	}
}

func TestRewriteEntryMissingEntry(t *testing.T) {
	deckPath, _ := packFixture(t)
	orig := readFile(t, deckPath)
	if err := RewriteEntry(deckPath, "nope.md", []byte("x"), time.Now()); err == nil {
		t.Fatal("RewriteEntry added a missing entry")
	}
	if err := RewriteEntry(deckPath, brand.ManifestName, []byte("{}"), time.Now()); err == nil {
		t.Fatal("RewriteEntry replaced the manifest")
	}
	if !bytes.Equal(readFile(t, deckPath), orig) {
		t.Error("original deck changed")
	}
	noTmp(t, deckPath)
}

func TestRewriteEntryKeepsMode(t *testing.T) {
	deckPath, _ := packFixture(t)
	if err := os.Chmod(deckPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RewriteEntry(deckPath, "slides.md", []byte("# x\n"), time.Now()); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(deckPath)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", st.Mode().Perm())
	}
}

func TestRenameRetries(t *testing.T) {
	oldRename, oldRetry, oldWait := rename, retryRename, renameWait
	t.Cleanup(func() { rename, retryRename, renameWait = oldRename, oldRetry, oldWait })
	retryRename, renameWait = true, 0

	calls := 0
	rename = func(a, b string) error {
		calls++
		if calls < 3 {
			return errors.New("sharing violation")
		}
		return nil
	}
	if err := replaceFile("a", "b"); err != nil || calls != 3 {
		t.Errorf("replaceFile = %v after %d calls, want nil after 3", err, calls)
	}

	calls = 0
	rename = func(a, b string) error { calls++; return errors.New("sharing violation") }
	if err := replaceFile("a", "b"); err == nil || calls != renameAttempts {
		t.Errorf("replaceFile = %v after %d calls, want an error after %d", err, calls, renameAttempts)
	}

	retryRename = false
	calls = 0
	if err := replaceFile("a", "b"); err == nil || calls != 1 {
		t.Errorf("without retry: %v after %d calls, want an error after 1", err, calls)
	}
}
