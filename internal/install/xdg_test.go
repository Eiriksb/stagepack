package install

import (
	"bytes"
	"encoding/xml"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"stagepack/assets"
	"stagepack/internal/brand"
)

func TestMimePackage(t *testing.T) {
	var doc struct {
		XMLName xml.Name `xml:"http://www.freedesktop.org/standards/shared-mime-info mime-info"`
		Types   []struct {
			Type    string `xml:"type,attr"`
			Comment string `xml:"comment"`
			Sub     struct {
				Type string `xml:"type,attr"`
			} `xml:"sub-class-of"`
			Icon struct {
				Name string `xml:"name,attr"`
			} `xml:"icon"`
			Glob struct {
				Pattern string `xml:"pattern,attr"`
				Weight  int    `xml:"weight,attr"`
			} `xml:"glob"`
		} `xml:"mime-type"`
	}
	if err := xml.Unmarshal([]byte(mimePackage()), &doc); err != nil {
		t.Fatalf("not well-formed: %v\n%s", err, mimePackage())
	}
	if len(doc.Types) != 1 {
		t.Fatalf("%d types, want 1", len(doc.Types))
	}
	m := doc.Types[0]
	if m.Type != brand.MIMEType || m.Sub.Type != "application/zip" || m.Icon.Name != brand.AppID {
		t.Errorf("type %q, subclass of %q, icon %q", m.Type, m.Sub.Type, m.Icon.Name)
	}
	// zip's own glob has the default weight, 50: ours must be above it.
	if m.Glob.Pattern != "*"+brand.Ext || m.Glob.Weight <= 50 {
		t.Errorf("glob %q weight %d", m.Glob.Pattern, m.Glob.Weight)
	}
	if !strings.Contains(m.Comment, brand.Name) {
		t.Errorf("comment %q", m.Comment)
	}
}

func desktopKeys(t *testing.T, entry string) map[string]string {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(entry, "\n"), "\n")
	if lines[0] != "[Desktop Entry]" {
		t.Fatalf("first line %q", lines[0])
	}
	keys := map[string]string{}
	for _, l := range lines[1:] {
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			t.Fatalf("not a key: %q", l)
		}
		if _, dup := keys[k]; dup {
			t.Fatalf("key %s twice", k)
		}
		keys[k] = v
	}
	return keys
}

func TestDesktopEntry(t *testing.T) {
	entry, err := desktopEntry("/home/åse/.local/share/stagepack/stagepack")
	if err != nil {
		t.Fatal(err)
	}
	k := desktopKeys(t, entry)
	want := map[string]string{
		"Type":     "Application",
		"Name":     brand.Name,
		"Exec":     `"/home/åse/.local/share/stagepack/stagepack" open %f`,
		"Icon":     brand.AppID,
		"MimeType": brand.MIMEType + ";",
		"Terminal": "false",
	}
	for key, v := range want {
		if k[key] != v {
			t.Errorf("%s=%q, want %q", key, k[key], v)
		}
	}
}

func TestDesktopExecArgEscapes(t *testing.T) {
	for in, want := range map[string]string{
		"/opt/a b/x":         `"/opt/a b/x"`,
		`/o"q/x`:             `"/o\\"q/x"`,
		"/o$HOME/x":          `"/o\\$HOME/x"`,
		"/o`id`/x":           "\"/o\\\\`id\\\\`/x\"",
		`/o\x`:               `"/o\\\\x"`,
		"/100%/x":            `"/100%%/x"`,
		"/semi;colon'quote/": `"/semi;colon'quote/"`,
	} {
		got, err := desktopExecArg(in)
		if err != nil || got != want {
			t.Errorf("desktopExecArg(%q) = %s, %v; want %s", in, got, err, want)
		}
	}
	for _, in := range []string{"/a\nExec=evil", "/a\tb", "/a\x00"} {
		if _, err := desktopExecArg(in); err == nil {
			t.Errorf("%q accepted", in)
		}
		if _, err := desktopEntry(in); err == nil {
			t.Errorf("desktop entry made for %q", in)
		}
	}
}

func TestIcoPNGsOfTheProgramIcon(t *testing.T) {
	pngs, err := icoPNGs(assets.Icon)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range iconSizes {
		img, err := png.Decode(bytes.NewReader(pngs[n]))
		if err != nil {
			t.Fatalf("%d px: %v", n, err)
		}
		if b := img.Bounds(); b.Dx() != n || b.Dy() != n {
			t.Errorf("%d px image is %v", n, b)
		}
	}
	for _, bad := range [][]byte{nil, []byte("PNG"), {0, 0, 1, 0, 1, 0}, {0, 0, 1, 0, 1, 0, 16, 16, 0, 0, 1, 0, 32, 0, 0xff, 0, 0, 0, 22, 0, 0, 0}} {
		if _, err := icoPNGs(bad); err == nil {
			t.Errorf("icoPNGs(%v) accepted", bad)
		}
	}
}

func TestHasNoKeys(t *testing.T) {
	for s, want := range map[string]bool{"": true, "\n[Default Applications]\n": true, "# c\n[A]\n\n": true, "[A]\nx=y\n": false} {
		if got := hasNoKeys(s); got != want {
			t.Errorf("hasNoKeys(%q) = %v", s, got)
		}
	}
}

func TestDropDefault(t *testing.T) {
	id, mime := desktopID, brand.MIMEType
	for _, c := range []struct{ in, want string }{
		{"[Default Applications]\n" + mime + "=" + id + "\ntext/plain=gedit.desktop\n",
			"[Default Applications]\ntext/plain=gedit.desktop\n"},
		{"[Added Associations]\n" + mime + "=other.desktop;" + id + ";\n[Default Applications]\n" + mime + "=" + id,
			"[Added Associations]\n" + mime + "=other.desktop;\n[Default Applications]\n"},
		{"[Removed Associations]\n" + mime + "=" + id + "\n", ""},
		{"[Default Applications]\n" + mime + "=other.desktop\n", ""},
		{"[Default Applications]\n" + mime + "x=" + id + "\n", ""},
	} {
		got, changed := dropDefault(c.in, mime, id)
		if c.want == "" {
			if changed || got != c.in {
				t.Errorf("dropDefault(%q) = %q, %v; want it unchanged", c.in, got, changed)
			}
			continue
		}
		if !changed || got != c.want {
			t.Errorf("dropDefault(%q) =\n%q, %v\nwant\n%q", c.in, got, changed, c.want)
		}
	}
}

// fakeDesktop is an xdgDesktop on temporary folders whose tools only record their calls;
// xdg-mime default writes mimeapps.list as the real one does.
type fakeDesktop struct {
	data, config string
	calls        []string
	x            *xdgDesktop
}

func newFakeDesktop(t *testing.T) *fakeDesktop {
	root := t.TempDir()
	f := &fakeDesktop{data: filepath.Join(root, "data"), config: filepath.Join(root, "config")}
	env := map[string]string{"HOME": root, "XDG_DATA_HOME": f.data, "XDG_CONFIG_HOME": f.config, "XDG_CURRENT_DESKTOP": "ubuntu:GNOME"}
	f.x = &xdgDesktop{
		getenv: func(k string) string { return env[k] },
		run: func(required bool, name string, args ...string) error {
			f.calls = append(f.calls, strings.Join(append([]string{name}, args...), " "))
			if name == "xdg-mime" {
				p := filepath.Join(f.config, "mimeapps.list")
				old, _ := os.ReadFile(p)
				return writeAtomic(p, append(old, []byte("[Default Applications]\n"+args[2]+"="+args[1]+"\n")...))
			}
			return nil
		},
	}
	return f
}

func TestXDGRegisterAndRemove(t *testing.T) {
	f := newFakeDesktop(t)
	// Things that are not ours, which must stay.
	write(t, f.data, "mime/packages/other.xml", "<mime-info/>")
	write(t, f.data, "icons/hicolor/48x48/apps/other.png", "png")
	write(t, f.config, "gnome-mimeapps.list", "[Default Applications]\ntext/plain=gedit.desktop\n")
	write(t, f.config, "mimeapps.list", "[Default Applications]\ntext/html=firefox.desktop\n")
	write(t, f.data, "applications/other.desktop", "[Desktop Entry]\n")
	write(t, f.data, "applications/mimeinfo.cache", "cache")

	exe := "/home/u/.local/share/stagepack/stagepack"
	if err := f.x.register(exe); err != nil {
		t.Fatal(err)
	}
	for _, rel := range ownedXDGFiles() {
		if !exists(f.data, rel) {
			t.Errorf("%s not written", rel)
		}
	}
	if got := read(t, f.data, mimeRel); got != mimePackage() {
		t.Errorf("MIME package = %q", got)
	}
	if k := desktopKeys(t, read(t, f.data, desktopRel)); k["Exec"] != `"`+exe+`" open %f` {
		t.Errorf("Exec=%s", k["Exec"])
	}
	want := []string{
		"update-mime-database " + filepath.Join(f.data, "mime"),
		"update-desktop-database " + filepath.Join(f.data, "applications"),
		"xdg-mime default " + desktopID + " " + brand.MIMEType,
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("tools run:\n%q\nwant\n%q", f.calls, want)
	}
	if got := read(t, f.config, "mimeapps.list"); !strings.Contains(got, brand.MIMEType+"="+desktopID) {
		t.Errorf("mimeapps.list = %q", got)
	}

	// A second register (a reinstall) writes the same and runs the tools again.
	f.calls = nil
	if err := f.x.register(exe); err != nil || len(f.calls) != 3 {
		t.Fatalf("reinstall: %v, %q", err, f.calls)
	}

	f.calls = nil
	if err := f.x.remove(); err != nil {
		t.Fatal(err)
	}
	for _, rel := range ownedXDGFiles() {
		if exists(f.data, rel) {
			t.Errorf("%s left behind", rel)
		}
	}
	if got := read(t, f.config, "mimeapps.list"); strings.Contains(got, brand.MIMEType) || !strings.Contains(got, "text/html=firefox.desktop") {
		t.Errorf("mimeapps.list = %q, want the other default only", got)
	}
	if got := read(t, f.config, "gnome-mimeapps.list"); got != "[Default Applications]\ntext/plain=gedit.desktop\n" {
		t.Errorf("another program's default was changed: %q", got)
	}
	for _, rel := range []string{"mime/packages/other.xml", "icons/hicolor/48x48/apps/other.png", "applications/mimeinfo.cache"} {
		if !exists(f.data, rel) {
			t.Errorf("%s, not ours, was removed", rel)
		}
	}
	for _, rel := range []string{"icons/hicolor/256x256", "icons/hicolor/16x16"} {
		if exists(f.data, rel) {
			t.Errorf("empty folder %s left behind", rel)
		}
	}
	want = []string{
		"update-mime-database " + filepath.Join(f.data, "mime"),
		"update-desktop-database " + filepath.Join(f.data, "applications"),
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("tools run on remove:\n%q\nwant\n%q", f.calls, want)
	}
}

func TestXDGRemoveDropsTheFoldersItMade(t *testing.T) {
	f := newFakeDesktop(t)
	if err := f.x.register("/x/stagepack"); err != nil {
		t.Fatal(err)
	}
	write(t, f.data, "mime/mime.cache", "made by update-mime-database")
	write(t, f.data, "applications/mimeinfo.cache", "made by update-desktop-database")
	f.calls = nil
	if err := f.x.remove(); err != nil {
		t.Fatal(err)
	}
	if exists(f.data, "mime") || exists(f.data, "icons") || exists(f.data, "applications") {
		t.Error("folders that held only our files are left behind")
	}
	if exists(f.config, "mimeapps.list") {
		t.Error("mimeapps.list, made for the default alone, is left behind")
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "update-mime-database") {
			t.Errorf("ran %s on a MIME folder that is gone", c)
		}
	}
}

func TestXDGRenewsAnIconCacheThatIsThere(t *testing.T) {
	f := newFakeDesktop(t)
	write(t, f.data, "icons/hicolor/icon-theme.cache", "cache")
	if err := f.x.register("/x/stagepack"); err != nil {
		t.Fatal(err)
	}
	want := "gtk-update-icon-cache -q -t -f " + filepath.Join(f.data, "icons", "hicolor")
	found := false
	for _, c := range f.calls {
		found = found || c == want
	}
	if !found {
		t.Errorf("calls %q lack %q", f.calls, want)
	}
}

func TestXDGDataHome(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, c := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"HOME": "/home/u"}, "/home/u/.local/share"},
		{map[string]string{"HOME": "/home/u", "XDG_DATA_HOME": "/d"}, "/d"},
		{map[string]string{"HOME": "/home/u", "XDG_DATA_HOME": "rel"}, "/home/u/.local/share"},
	} {
		if got, err := XDGDataHome(env(c.env)); err != nil || got != filepath.FromSlash(c.want) {
			t.Errorf("XDGDataHome(%v) = %q, %v", c.env, got, err)
		}
	}
	if _, err := XDGDataHome(env(nil)); err == nil {
		t.Error("no HOME: no error")
	}
}

// The whole Linux install and uninstall, on temporary folders.
func TestLinuxInstallAndUninstall(t *testing.T) {
	f := newFakeDesktop(t)
	home := filepath.Join(f.data, brand.AppID)
	self := filepath.Join(t.TempDir(), "stagepack")
	write(t, filepath.Dir(self), "stagepack", "ELF")
	if err := install(home, payloadFS("1"), self, f.x); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(home, ExeName())
	if fi, err := os.Stat(exe); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("program not installed as a program: %v, %v", fi, err)
	}
	if k := desktopKeys(t, read(t, f.data, desktopRel)); !strings.Contains(k["Exec"], exe) {
		t.Errorf("Exec=%s, want the installed program", k["Exec"])
	}
	write(t, home, "journal/x.json", "notes")
	if _, err := uninstall(home, "", f.x); err != nil {
		t.Fatal(err)
	}
	if exists(f.data, desktopRel) || exists(f.data, "mime") || exists(home, ExeName()) || exists(home, "node") {
		t.Error("uninstall left files behind")
	}
	if !exists(home, "journal/x.json") {
		t.Error("the journal was removed")
	}
}
