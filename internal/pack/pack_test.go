package pack

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stagepack/internal/brand"
	"stagepack/internal/deck"
)

const fixture = "../../testdata/deck"

// copyDir copies src to a new temp dir (symlinks followed) and returns it.
func copyDir(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func packTo(t *testing.T, o Options) (deck.Manifest, string, error) {
	t.Helper()
	if o.Out == "" {
		o.Out = filepath.Join(t.TempDir(), "talk"+brand.Ext)
	}
	m, err := Pack(o)
	return m, o.Out, err
}

func openZip(t *testing.T, path string) *zip.ReadCloser {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { zr.Close() })
	return zr
}

func entries(zr *zip.ReadCloser) map[string]*zip.File {
	m := map[string]*zip.File{}
	for _, f := range zr.File {
		m[f.Name] = f
	}
	return m
}

func content(t *testing.T, f *zip.File) string {
	t.Helper()
	rc, err := f.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPackLayout(t *testing.T) {
	m, out, err := packTo(t, Options{Dir: fixture, Warn: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	got := entries(openZip(t, out))
	for _, name := range []string{
		brand.ManifestName, "slides.md", "style.css", "public/clip.mp4", "package.json",
		"vendor/node_modules/fixture-dep/package.json",
		"vendor/node_modules/fixture-dep-child/package.json",
	} {
		if got[name] == nil {
			t.Errorf("missing entry %s", name)
		}
	}
	for name := range got {
		if strings.HasPrefix(name, "node_modules/") || strings.Contains(name, "playwright-chromium") ||
			strings.Contains(name, "@fontsource-variable/inter") || strings.Contains(name, "@slidev/cli") {
			t.Errorf("unexpected entry %s", name)
		}
		if strings.Contains(name, "\\") {
			t.Errorf("entry %q has a backslash", name)
		}
	}
	if m.Title != "Fixture talk" || m.Entry != "slides.md" || m.Slidev != "53.0.0" ||
		len(m.ID) != 36 || m.Format != deck.FormatVersion {
		t.Errorf("manifest = %+v", m)
	}
	zr := openZip(t, out)
	fromZip, err := deck.ReadManifest(&zr.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if fromZip.ID != m.ID || fromZip.Title != m.Title {
		t.Errorf("manifest in zip = %+v, returned %+v", fromZip, m)
	}
}

func TestPackMetaAndTitleOption(t *testing.T) {
	m, _, err := packTo(t, Options{Dir: fixture, Title: "Override", Meta: map[string]string{"commit": "abc"}, Warn: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Override" || m.Meta["commit"] != "abc" {
		t.Errorf("manifest = %+v", m)
	}
}

func TestPackTitleFallsBackToFolderName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my-talk")
	write(t, filepath.Join(dir, "slides.md"), "# No front matter\n")
	write(t, filepath.Join(dir, "package.json"), `{"devDependencies":{"@slidev/cli":"^53.0.0"}}`)
	m, _, err := packTo(t, Options{Dir: dir, Warn: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "my-talk" {
		t.Errorf("Title = %q, want my-talk", m.Title)
	}
}

func TestPackStoresMediaDeflatesText(t *testing.T) {
	_, out, err := packTo(t, Options{Dir: fixture, Warn: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	got := entries(openZip(t, out))
	if m := got["public/clip.mp4"].Method; m != zip.Store {
		t.Errorf("clip.mp4 method = %d, want Store", m)
	}
	if m := got["slides.md"].Method; m != zip.Deflate {
		t.Errorf("slides.md method = %d, want Deflate", m)
	}
	if m := got["vendor/node_modules/fixture-dep/index.css"].Method; m != zip.Deflate {
		t.Errorf("vendored css method = %d, want Deflate", m)
	}
}

func TestPackFollowsSymlinks(t *testing.T) {
	dir := copyDir(t, fixture)
	outside := filepath.Join(t.TempDir(), "real.mp4")
	write(t, outside, "the target bytes")
	if err := os.MkdirAll(filepath.Join(dir, "public", "video"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "public", "video", "v.mp4")); err != nil {
		t.Fatal(err)
	}
	_, out, err := packTo(t, Options{Dir: dir, Warn: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	f := entries(openZip(t, out))["public/video/v.mp4"]
	if f == nil {
		t.Fatal("public/video/v.mp4 missing")
	}
	if !f.Mode().IsRegular() {
		t.Errorf("mode = %v, want a regular file", f.Mode())
	}
	if got := content(t, f); got != "the target bytes" {
		t.Errorf("content = %q", got)
	}
}

func TestPackExclude(t *testing.T) {
	dir := copyDir(t, fixture)
	write(t, filepath.Join(dir, "public", "private", "clip.mp4"), "private clip")
	write(t, filepath.Join(dir, "public", "privateish.txt"), "not excluded")
	_, out, err := packTo(t, Options{Dir: dir, Exclude: []string{"public/private"}, Warn: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	got := entries(openZip(t, out))
	for name := range got {
		if strings.HasPrefix(name, "public/private/") {
			t.Errorf("excluded entry %s present", name)
		}
	}
	if got["public/privateish.txt"] == nil {
		t.Error("public/privateish.txt was excluded; a prefix must match whole path components")
	}
}

func TestPackSkipsToolFolders(t *testing.T) {
	dir := copyDir(t, fixture)
	write(t, filepath.Join(dir, ".git", "HEAD"), "ref")
	write(t, filepath.Join(dir, "dist", "index.html"), "x")
	write(t, filepath.Join(dir, ".slidev", "x.json"), "{}")
	_, out, err := packTo(t, Options{Dir: dir, Warn: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	for name := range entries(openZip(t, out)) {
		if strings.HasPrefix(name, ".git/") || strings.HasPrefix(name, "dist/") || strings.HasPrefix(name, ".slidev/") {
			t.Errorf("unexpected entry %s", name)
		}
	}
}

func setSlidevVersion(t *testing.T, dir, version string) {
	t.Helper()
	write(t, filepath.Join(dir, "node_modules", "@slidev", "cli", "package.json"),
		`{"name":"@slidev/cli","version":"`+version+`"}`)
}

func TestPackRefusesSlidevMajor(t *testing.T) {
	dir := copyDir(t, fixture)
	setSlidevVersion(t, dir, "54.1.0")
	_, out, err := packTo(t, Options{Dir: dir, Warn: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "Slidev 54") || !strings.Contains(err.Error(), "runtime has 53") {
		t.Fatalf("err = %v, want one naming Slidev 54 and runtime has 53", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Errorf("output file left behind (stat err = %v)", statErr)
	}
	if _, statErr := os.Stat(out + ".tmp"); !os.IsNotExist(statErr) {
		t.Errorf("temp file left behind (stat err = %v)", statErr)
	}
}

func TestPackWarnsSlidevMinor(t *testing.T) {
	dir := copyDir(t, fixture)
	setSlidevVersion(t, dir, "53.2.0")
	var warn bytes.Buffer
	m, _, err := packTo(t, Options{Dir: dir, Warn: &warn})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warn.String(), "53.2.0") || !strings.Contains(warn.String(), "53.0.0") {
		t.Errorf("warning = %q, want both versions", warn.String())
	}
	if m.Slidev != "53.2.0" {
		t.Errorf("Slidev = %q, want 53.2.0", m.Slidev)
	}
}

func TestPackRefusesNativeDep(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"node file": func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "node_modules", "fixture-dep", "build", "x.node"), "ELF")
		},
		"os field": func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "node_modules", "fixture-dep", "package.json"),
				`{"name":"fixture-dep","version":"1.0.0","os":["linux"],"dependencies":{"fixture-dep-child":"^1.0.0"}}`)
		},
		"cpu field": func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "node_modules", "fixture-dep", "package.json"),
				`{"name":"fixture-dep","version":"1.0.0","cpu":["x64"]}`)
		},
		"binding.gyp": func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "node_modules", "fixture-dep", "binding.gyp"), "{}")
		},
		"postinstall": func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "node_modules", "fixture-dep", "package.json"),
				`{"name":"fixture-dep","version":"1.0.0","scripts":{"postinstall":"node build.js"}}`)
		},
		"install script": func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "node_modules", "fixture-dep", "package.json"),
				`{"name":"fixture-dep","version":"1.0.0","scripts":{"install":"node-gyp rebuild"}}`)
		},
		"native in child": func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "node_modules", "fixture-dep-child", "x.node"), "ELF")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			dir := copyDir(t, fixture)
			mutate(t, dir)
			_, out, err := packTo(t, Options{Dir: dir, Warn: io.Discard})
			if err == nil {
				t.Fatal("want an error")
			}
			want := "fixture-dep"
			if name == "native in child" {
				want = "fixture-dep-child"
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %s", err, want)
			}
			if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
				t.Errorf("output left behind")
			}
		})
	}
}

func TestPackVendorsScopedPackages(t *testing.T) {
	dir := copyDir(t, fixture)
	write(t, filepath.Join(dir, "package.json"),
		`{"devDependencies":{"@slidev/cli":"^53.0.0","@scope/pkg":"^1.0.0"}}`)
	write(t, filepath.Join(dir, "node_modules", "@scope", "pkg", "package.json"), `{"name":"@scope/pkg","version":"1.0.0"}`)
	write(t, filepath.Join(dir, "node_modules", "@scope", "pkg", "lib", "a.js"), "export {}")
	_, out, err := packTo(t, Options{Dir: dir, Warn: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	got := entries(openZip(t, out))
	for _, name := range []string{"vendor/node_modules/@scope/pkg/package.json", "vendor/node_modules/@scope/pkg/lib/a.js"} {
		if got[name] == nil {
			t.Errorf("missing %s", name)
		}
	}
	if got["vendor/node_modules/fixture-dep/package.json"] != nil {
		t.Error("fixture-dep vendored though the project no longer depends on it")
	}
}

func TestPackRefusesMissingDependency(t *testing.T) {
	dir := copyDir(t, fixture)
	if err := os.RemoveAll(filepath.Join(dir, "node_modules", "fixture-dep")); err != nil {
		t.Fatal(err)
	}
	_, _, err := packTo(t, Options{Dir: dir, Warn: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "fixture-dep") {
		t.Fatalf("err = %v, want one naming fixture-dep", err)
	}
}

func TestPackOutputAtomic(t *testing.T) {
	dir := copyDir(t, fixture)
	out := filepath.Join(t.TempDir(), "talk"+brand.Ext)
	if _, err := Pack(Options{Dir: dir, Out: out, Warn: io.Discard}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	setSlidevVersion(t, dir, "54.0.0")
	if _, err := Pack(Options{Dir: dir, Out: out, Warn: io.Discard}); err == nil {
		t.Fatal("want an error")
	}
	after, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("the old output changed")
	}
	if _, err := os.Stat(out + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file left behind")
	}
}

func TestPackOutputInsideDirIsNotPacked(t *testing.T) {
	dir := copyDir(t, fixture)
	out := filepath.Join(dir, "talk"+brand.Ext)
	for i := 0; i < 2; i++ {
		if _, err := Pack(Options{Dir: dir, Out: out, Warn: io.Discard}); err != nil {
			t.Fatal(err)
		}
	}
	for name := range entries(openZip(t, out)) {
		if strings.HasSuffix(name, brand.Ext) || strings.HasSuffix(name, ".tmp") {
			t.Errorf("packed its own output: %s", name)
		}
	}
}

func TestPackMissingEntry(t *testing.T) {
	_, _, err := packTo(t, Options{Dir: fixture, Entry: "nope.md", Warn: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "nope.md") {
		t.Fatalf("err = %v", err)
	}
}

// packedEntry packs dir and returns the packed entry text (flagging a missing favicon svg).
func packedEntry(t *testing.T, dir string) string {
	t.Helper()
	_, out, err := packTo(t, Options{Dir: dir, Warn: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	got := entries(openZip(t, out))
	if got[faviconPath] == nil {
		return content(t, got["slides.md"]) + "\n<<no svg>>"
	}
	return content(t, got["slides.md"])
}

func TestPackFavicon(t *testing.T) {
	before, err := os.ReadFile(filepath.Join(fixture, "slides.md"))
	if err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	_, out, err := packTo(t, Options{Dir: fixture, Warn: &warn})
	if err != nil {
		t.Fatal(err)
	}
	got := entries(openZip(t, out))
	slides := content(t, got["slides.md"])
	if !strings.HasPrefix(slides, "---\n") || !strings.Contains(strings.SplitN(slides, "\n---\n", 2)[0], "\nfavicon: "+faviconURL) {
		t.Errorf("headmatter lacks the favicon line:\n%s", slides)
	}
	if !strings.Contains(slides, "title: Fixture talk") || !strings.Contains(slides, "# Second slide") {
		t.Errorf("rest of the entry was lost:\n%s", slides)
	}
	svg := got[faviconPath]
	if svg == nil {
		t.Fatal(faviconPath + " missing")
	}
	if s := content(t, svg); !strings.HasPrefix(s, "<svg") {
		t.Errorf("svg = %q", s)
	}
	if strings.Count(warn.String(), "\n") != 1 || !strings.Contains(warn.String(), "favicon") {
		t.Errorf("warn = %q, want one line about the favicon", warn.String())
	}
	after, err := os.ReadFile(filepath.Join(fixture, "slides.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("the author's slides.md was modified")
	}
}

func TestPackFaviconRemoteReplaced(t *testing.T) {
	dir := copyDir(t, fixture)
	write(t, filepath.Join(dir, "slides.md"), "---\ntheme: default\ntitle: Remote\nfavicon: https://example.com/f.png\n---\n\n# One\n")
	slides := packedEntry(t, dir)
	if !strings.Contains(slides, "favicon: "+faviconURL) || strings.Contains(slides, "example.com") {
		t.Errorf("remote favicon not replaced:\n%s", slides)
	}
	if strings.Count(slides, "favicon:") != 1 {
		t.Errorf("want exactly one favicon line:\n%s", slides)
	}
}

func TestPackFaviconLocalKept(t *testing.T) {
	dir := copyDir(t, fixture)
	orig := "---\ntheme: default\ntitle: Local\nfavicon: /favicon.png\nfonts:\n  provider: none\n---\n\n# One\n"
	write(t, filepath.Join(dir, "slides.md"), orig)
	var warn bytes.Buffer
	_, out, err := packTo(t, Options{Dir: dir, Warn: &warn})
	if err != nil {
		t.Fatal(err)
	}
	got := entries(openZip(t, out))
	if s := content(t, got["slides.md"]); s != orig {
		t.Errorf("entry changed:\n%s", s)
	}
	if got[faviconPath] != nil {
		t.Error("favicon svg added though the deck has a local favicon")
	}
	if warn.Len() != 0 {
		t.Errorf("warn = %q, want nothing", warn.String())
	}
}

func TestPackFaviconNoHeadmatter(t *testing.T) {
	dir := copyDir(t, fixture)
	write(t, filepath.Join(dir, "slides.md"), "# Just a slide\n")
	slides := packedEntry(t, dir)
	if !strings.HasPrefix(slides, "---\nfavicon: "+faviconURL+"\n---\n") || !strings.HasSuffix(strings.TrimSuffix(slides, "\n"), "# Just a slide") {
		t.Errorf("slides = %q", slides)
	}
}

func TestPackFaviconCRLF(t *testing.T) {
	dir := copyDir(t, fixture)
	write(t, filepath.Join(dir, "slides.md"), "---\r\ntitle: Win\r\n---\r\n\r\n# One\r\n")
	slides := packedEntry(t, dir)
	if !strings.HasPrefix(slides, "---\r\nfavicon: "+faviconURL+"\r\ntitle: Win\r\n---\r\n") {
		t.Errorf("slides = %q", slides)
	}
}

// refusesName packs a project with one extra file and wants an error naming it, and no output.
func refusesName(t *testing.T, files map[string]string, want string) {
	t.Helper()
	dir := copyDir(t, fixture)
	for name, body := range files {
		write(t, filepath.Join(dir, filepath.FromSlash(name)), body)
	}
	_, out, err := packTo(t, Options{Dir: dir, Warn: io.Discard})
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want one naming %q", err, want)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Errorf("output file written (stat err = %v)", statErr)
	}
	if _, statErr := os.Stat(out + ".tmp"); !os.IsNotExist(statErr) {
		t.Errorf("temp file left behind (stat err = %v)", statErr)
	}
}

func TestPackRefusesWindowsUnsafeName(t *testing.T) {
	for name, want := range map[string]string{
		"notes: draft.md": "notes: draft.md",
		"what?.md":        "what?.md",
		"aux.js":          "aux.js",
		"trailing.":       "trailing.",
	} {
		t.Run(name, func(t *testing.T) { refusesName(t, map[string]string{name: "x"}, want) })
	}
}

func TestPackRefusesCaseCollision(t *testing.T) {
	refusesName(t, map[string]string{"Intro.md": "a", "intro.md": "b"}, "ntro.md")
}

func TestPackRefusesUnsafeVendoredName(t *testing.T) {
	dir := copyDir(t, fixture)
	write(t, filepath.Join(dir, "node_modules", "fixture-dep", "README.md"), "a")
	write(t, filepath.Join(dir, "node_modules", "fixture-dep", "readme.md"), "b")
	_, out, err := packTo(t, Options{Dir: dir, Warn: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "fixture-dep/") {
		t.Fatalf("err = %v, want one naming the vendored file", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Errorf("output file written (stat err = %v)", statErr)
	}
}

func TestPackRefusesManifestNameClash(t *testing.T) {
	refusesName(t, map[string]string{brand.ManifestName: "{}"}, brand.ManifestName)
}

func TestPackRefusesEntryTheViewerCannotOpen(t *testing.T) {
	dir := copyDir(t, fixture)
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "x.md"), []byte("# x\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "-x.md"), []byte("# x\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "x.txt"), []byte("# x\n"), 0o644)
	for _, e := range []string{"sub/x.md", "-x.md", "x.txt"} {
		if _, _, err := packTo(t, Options{Dir: dir, Entry: e, Warn: io.Discard}); err == nil {
			t.Errorf("entry %q packed", e)
		}
	}
}

func TestPackWarnsOnlineFonts(t *testing.T) {
	for _, tc := range []struct {
		name, head string
		warn       bool
	}{
		{"no fonts key", "title: T\n", true},
		{"google provider", "fonts:\n  sans: Roboto\n  provider: google\n", true},
		{"provider none", "fonts:\n  sans: Inter Variable\n  provider: none\n", false},
		{"provider none quoted, with a comment", "fonts:\n  provider: 'none' # bundled\n", false},
		{"provider none inline", "fonts: { provider: none }\n", false},
		{"provider at the top level is not fonts.provider", "provider: none\nfonts:\n  sans: Inter\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := copyDir(t, fixture)
			write(t, filepath.Join(dir, "slides.md"), "---\nfavicon: /f.svg\n"+tc.head+"---\n\n# One\n")
			var warn bytes.Buffer
			if _, _, err := packTo(t, Options{Dir: dir, Warn: &warn}); err != nil {
				t.Fatal(err)
			}
			got := strings.Contains(warn.String(), "provider: none")
			if got != tc.warn {
				t.Errorf("warned = %v, want %v; warn = %q", got, tc.warn, warn.String())
			}
			if tc.warn && strings.Count(warn.String(), "\n") != 1 {
				t.Errorf("want one warning line, got %q", warn.String())
			}
		})
	}
}

func TestPackHeadmatterBOM(t *testing.T) {
	dir := copyDir(t, fixture)
	write(t, filepath.Join(dir, "slides.md"), "\uFEFF---\r\ntitle: Bom talk\r\n---\r\n\r\n# One\r\n")
	m, out, err := packTo(t, Options{Dir: dir, Warn: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Bom talk" {
		t.Errorf("Title = %q, want Bom talk", m.Title)
	}
	slides := content(t, entries(openZip(t, out))["slides.md"])
	if want := "\uFEFF---\r\nfavicon: " + faviconURL + "\r\ntitle: Bom talk\r\n---\r\n"; !strings.HasPrefix(slides, want) {
		t.Errorf("slides = %q, want prefix %q", slides, want)
	}
}

func TestPackHeadmatterFenceTrailingSpace(t *testing.T) {
	dir := copyDir(t, fixture)
	write(t, filepath.Join(dir, "slides.md"), "--- \ntitle: Spaced\nfonts:\n  provider: none\n---\t\n\n# One\n")
	var warn bytes.Buffer
	m, out, err := packTo(t, Options{Dir: dir, Warn: &warn})
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Spaced" {
		t.Errorf("Title = %q, want Spaced", m.Title)
	}
	slides := content(t, entries(openZip(t, out))["slides.md"])
	if want := "--- \nfavicon: " + faviconURL + "\ntitle: Spaced\n"; !strings.HasPrefix(slides, want) {
		t.Errorf("slides = %q, want prefix %q", slides, want)
	}
	if strings.Contains(warn.String(), "provider: none") {
		t.Errorf("fonts warning though the headmatter sets provider: none: %q", warn.String())
	}
}

func TestPackFaviconProtocolRelativeReplaced(t *testing.T) {
	dir := copyDir(t, fixture)
	write(t, filepath.Join(dir, "slides.md"), "---\ntitle: PR\nfavicon: //cdn.example.com/f.png\n---\n\n# One\n")
	slides := packedEntry(t, dir)
	if !strings.Contains(slides, "favicon: "+faviconURL) || strings.Contains(slides, "example.com") || strings.Contains(slides, "<<no svg>>") {
		t.Errorf("protocol-relative favicon not replaced:\n%s", slides)
	}
}

func TestPackFaviconEmptyIsMissing(t *testing.T) {
	for _, line := range []string{"favicon:", "favicon: ", `favicon: ""`, "favicon: ''"} {
		dir := copyDir(t, fixture)
		write(t, filepath.Join(dir, "slides.md"), "---\ntitle: E\n"+line+"\n---\n\n# One\n")
		slides := packedEntry(t, dir)
		if !strings.Contains(slides, "favicon: "+faviconURL) || strings.Count(slides, "favicon:") != 1 || strings.Contains(slides, "<<no svg>>") {
			t.Errorf("%q: empty favicon not replaced:\n%s", line, slides)
		}
	}
}

func TestPackNamesTheCode(t *testing.T) {
	var warn bytes.Buffer
	if _, _, err := packTo(t, Options{Dir: fixture, Warn: &warn}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(warn.String(), "contains code") {
		t.Errorf("the fixture (CSS-only package, no scripts) said it contains code: %q", warn.String())
	}

	dir := copyDir(t, fixture)
	write(t, filepath.Join(dir, "vite.config.ts"), "export default {}\n")
	write(t, filepath.Join(dir, "components", "Counter.vue"), "<template>1</template>\n")
	write(t, filepath.Join(dir, "public", "player.js"), "1\n")
	warn.Reset()
	if _, _, err := packTo(t, Options{Dir: dir, Warn: &warn}); err != nil {
		t.Fatal(err)
	}
	out := warn.String()
	for _, want := range []string{"contains code", "\n  components/Counter.vue\n", "\n  vite.config.ts (configuration that runs as code)\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("warnings lack %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "player.js") {
		t.Errorf("public/player.js named as code:\n%s", out)
	}
}
