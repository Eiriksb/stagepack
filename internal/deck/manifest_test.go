package deck

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"stagepack/internal/brand"
)

func zipWith(t *testing.T, files map[string][]byte) *zip.Reader {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	return zr
}

func TestReadManifestRoundTrip(t *testing.T) {
	want := Manifest{
		Format:  FormatVersion,
		ID:      "3f2a",
		Title:   "Example talk",
		Created: time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC),
		Updated: time.Date(2026, 10, 22, 10, 30, 0, 0, time.UTC),
		Slidev:  "53.0.0",
		Entry:   "slides.md",
		Meta:    map[string]string{"commit": "abc"},
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadManifest(zipWith(t, map[string][]byte{brand.ManifestName: data}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Format != want.Format || got.ID != want.ID || got.Title != want.Title ||
		!got.Created.Equal(want.Created) || !got.Updated.Equal(want.Updated) ||
		got.Slidev != want.Slidev || got.Entry != want.Entry {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got.Meta["commit"] != "abc" {
		t.Errorf("Meta[commit] = %q, want abc", got.Meta["commit"])
	}
}

func TestManifestJSONKeys(t *testing.T) {
	data, err := json.Marshal(Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"format", "id", "title", "created", "updated", "slidev", "entry", "meta"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing JSON key %q in %s", k, data)
		}
	}
}

func TestReadManifestMissing(t *testing.T) {
	_, err := ReadManifest(zipWith(t, map[string][]byte{"slides.md": []byte("# hi")}))
	if err == nil || !strings.Contains(err.Error(), brand.ManifestName) {
		t.Fatalf("err = %v, want one mentioning %s", err, brand.ManifestName)
	}
}

func TestReadManifestBadJSON(t *testing.T) {
	_, err := ReadManifest(zipWith(t, map[string][]byte{brand.ManifestName: []byte("{nope")}))
	if err == nil {
		t.Fatal("want an error for malformed JSON")
	}
}

func TestCheckAcceptsMinorDiff(t *testing.T) {
	if err := Check(Manifest{Format: 1, Slidev: "53.4.0"}, "53.0.0"); err != nil {
		t.Fatal(err)
	}
}

func TestCheckRefusesMajorDiff(t *testing.T) {
	err := Check(Manifest{Format: 1, Slidev: "54.0.0"}, "53.0.0")
	var tn *TooNewError
	if !errors.As(err, &tn) {
		t.Fatalf("err = %v (%T), want *TooNewError", err, err)
	}
	if want := "newer " + brand.Name + " viewer"; !strings.Contains(err.Error(), want) {
		t.Errorf("message %q lacks %q", err, want)
	}
}

func TestCheckRefusesNewerFormat(t *testing.T) {
	err := Check(Manifest{Format: 2, Slidev: "53.0.0"}, "53.0.0")
	var tn *TooNewError
	if !errors.As(err, &tn) {
		t.Fatalf("err = %v (%T), want *TooNewError", err, err)
	}
}

func TestMajor(t *testing.T) {
	for in, want := range map[string]int{"53.0.0": 53, "^53.0.0": 53, "~7.1.2": 7, "v12.0.0": 12} {
		got, err := Major(in)
		if err != nil || got != want {
			t.Errorf("Major(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "x.y", "^"} {
		if _, err := Major(in); err == nil {
			t.Errorf("Major(%q): want error", in)
		}
	}
}

func TestCheckRefusesOlderMajor(t *testing.T) {
	err := Check(Manifest{Format: 1, Slidev: "52.1.0"}, "53.0.0")
	var tn *TooNewError
	if !errors.As(err, &tn) {
		t.Fatalf("err = %v (%T), want *TooNewError", err, err)
	}
	if !tn.Older {
		t.Error("Older = false, want true")
	}
	msg := err.Error()
	for _, want := range []string{"Slidev 52", "Slidev 53", "repacked", brand.Name} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "newer "+brand.Name+" viewer") {
		t.Errorf("message %q must not tell the user to get a newer viewer", msg)
	}
}

func TestCheckNewerMajorIsNotOlder(t *testing.T) {
	err := Check(Manifest{Format: 1, Slidev: "54.0.0"}, "53.0.0")
	var tn *TooNewError
	if !errors.As(err, &tn) || tn.Older {
		t.Fatalf("err = %v, want *TooNewError with Older=false", err)
	}
}

func TestReadManifestTooLarge(t *testing.T) {
	big := []byte(`{"title":"` + strings.Repeat("x", maxManifestSize) + `"}`)
	_, err := ReadManifest(zipWith(t, map[string][]byte{brand.ManifestName: big}))
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v, want a 'too large' error", err)
	}
}

func TestCheckEntryName(t *testing.T) {
	for _, ok := range []string{"slides.md", "public/clip.mp4", "vendor/node_modules/@scope/pkg/a.js", "dir/", "a b/c d.md", ".slidev/x", "é.md"} {
		if err := CheckEntryName(ok); err != nil {
			t.Errorf("CheckEntryName(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{
		"", "/abs", "../x", "a/../b", "a/./b", "a//b", `a\b`, "C:/x", "x:y", "a<b", `a"b`, "a|b", "a?b", "a*b",
		"CON", "a/Nul.txt", "com1", "LPT9.md", "end.", "end ", "a\x01b",
	} {
		if err := CheckEntryName(bad); err == nil {
			t.Errorf("CheckEntryName(%q) = nil, want an error", bad)
		}
	}
}

func TestCheckEntryNamesSet(t *testing.T) {
	paths, dirs, err := CheckEntryNames([]string{brand.ManifestName, "slides.md", "vendor/node_modules/x/a.js", "public/", "public/b.png"})
	if err != nil {
		t.Fatal(err)
	}
	if paths[2] != "node_modules/x/a.js" || !dirs[3] || dirs[1] {
		t.Errorf("paths %v dirs %v", paths, dirs)
	}
	for name, names := range map[string][]string{
		"duplicate":    {"a.md", "a.md"},
		"case":         {"a.md", "A.md"},
		"vendor clash": {"node_modules/x", "vendor/node_modules/x"},
		"file, dir":    {"a", "a/b"},
		"dir, file":    {"a/b", "a"},
		"case parent":  {"A", "a/b"},
	} {
		if _, _, err := CheckEntryNames(names); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
