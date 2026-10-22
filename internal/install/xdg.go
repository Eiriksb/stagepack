package install

// The file type on Linux desktops (freedesktop.org): a MIME package that makes
// *.sptx its own type instead of a zip, a .desktop entry that opens it with the
// viewer, the icon in the hicolor theme, and the viewer as the type's default.
// Everything is per user, below $XDG_DATA_HOME and $XDG_CONFIG_HOME.
//
// The generated files are plain functions of their input, so they are tested
// on any OS; the desktop's own tools (update-mime-database, xdg-mime, …) run
// as separate programs.

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"stagepack/assets"
	"stagepack/internal/brand"
)

const desktopID = brand.AppID + ".desktop"

// iconSizes are the sizes of the program icon (assets/stagepack.ico), each
// installed as icons/hicolor/<n>x<n>/apps/<AppID>.png.
var iconSizes = []int{256, 48, 32, 16}

// XDGDataHome is $XDG_DATA_HOME, or ~/.local/share when that is unset or not
// absolute (as the spec says to treat it).
func XDGDataHome(getenv func(string) string) (string, error) {
	return xdgDir(getenv, "XDG_DATA_HOME", ".local/share")
}

func xdgConfigHome(getenv func(string) string) (string, error) {
	return xdgDir(getenv, "XDG_CONFIG_HOME", ".config")
}

func xdgDir(getenv func(string) string, v, fallback string) (string, error) {
	if d := getenv(v); filepath.IsAbs(d) {
		return d, nil
	}
	home := getenv("HOME")
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("neither %s nor HOME is set to a full path", v)
	}
	return filepath.Join(home, filepath.FromSlash(fallback)), nil
}

// mimePackage is mime/packages/<AppID>.xml. The type is a subclass of zip, so
// that a .sptx file, which is a zip by its content, still counts as its own
// type: when the glob and the content disagree, the spec takes the glob's
// type if it is a subclass of what the content says. The weight puts the glob
// above zip's own.
func mimePackage() string {
	esc := func(s string) string {
		var b strings.Builder
		xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<mime-info xmlns="http://www.freedesktop.org/standards/shared-mime-info">
  <mime-type type="` + esc(brand.MIMEType) + `">
    <comment>` + esc(brand.Name+" talk") + `</comment>
    <sub-class-of type="application/zip"/>
    <icon name="` + esc(brand.AppID) + `"/>
    <generic-icon name="x-office-presentation"/>
    <glob pattern="*` + esc(brand.Ext) + `" weight="90"/>
  </mime-type>
</mime-info>
`
}

// desktopEntry is applications/<AppID>.desktop: it opens a talk with the
// program at exe. It is not shown in the application menu (started alone the
// viewer has nothing to show).
func desktopEntry(exe string) (string, error) {
	arg, err := desktopExecArg(exe)
	if err != nil {
		return "", err
	}
	return "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=" + brand.Name + "\n" +
		"GenericName=Talk viewer\n" +
		"Comment=Present a " + brand.Name + " talk (" + brand.Ext + ") in the presenter view, offline\n" +
		"Exec=" + arg + " open %f\n" +
		"Icon=" + brand.AppID + "\n" +
		"MimeType=" + brand.MIMEType + ";\n" +
		"Terminal=false\n" +
		"NoDisplay=true\n" +
		"StartupNotify=false\n" +
		"Categories=Office;Presentation;\n", nil
}

// desktopExecArg quotes p as one argument of a desktop entry's Exec key: inside
// double quotes, with " ` $ \ escaped by a backslash; then, as the value is a
// string too, every backslash doubled; and % written as %%. A path with a line
// break or another control character cannot be written there.
func desktopExecArg(p string) (string, error) {
	if strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", fmt.Errorf("the install folder holds a control character: %q", p)
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range p {
		switch r {
		case '"', '`', '$':
			b.WriteString(`\\`)
			b.WriteRune(r)
		case '\\':
			b.WriteString(`\\\\`)
		case '%':
			b.WriteString("%%")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String(), nil
}

// icoPNGs returns the PNG images of an .ico by their size (an .ico entry's
// width byte 0 means 256). Entries that are not PNG are skipped.
func icoPNGs(ico []byte) (map[int][]byte, error) {
	if len(ico) < 6 || binary.LittleEndian.Uint16(ico[0:]) != 0 || binary.LittleEndian.Uint16(ico[2:]) != 1 {
		return nil, errors.New("not an .ico file")
	}
	n := int(binary.LittleEndian.Uint16(ico[4:]))
	out := map[int][]byte{}
	for i := 0; i < n; i++ {
		e := 6 + 16*i
		if e+16 > len(ico) {
			return nil, errors.New("the .ico directory is cut short")
		}
		w := int(ico[e])
		if w == 0 {
			w = 256
		}
		size := int(binary.LittleEndian.Uint32(ico[e+8:]))
		off := int(binary.LittleEndian.Uint32(ico[e+12:]))
		if off < 0 || size < 0 || off > len(ico) || size > len(ico)-off {
			return nil, fmt.Errorf(".ico image %d lies outside the file", i)
		}
		img := ico[off : off+size]
		if bytes.HasPrefix(img, []byte("\x89PNG\r\n\x1a\n")) {
			out[w] = img
		}
	}
	return out, nil
}

// xdgFile is one file Install writes below the data folder.
type xdgFile struct {
	rel  string // slash-separated, below $XDG_DATA_HOME
	data []byte
}

func iconRel(n int) string {
	return "icons/hicolor/" + strconv.Itoa(n) + "x" + strconv.Itoa(n) + "/apps/" + brand.AppID + ".png"
}

const (
	mimeRel    = "mime/packages/" + brand.AppID + ".xml"
	desktopRel = "applications/" + desktopID
)

// xdgFiles lists what Install writes for the program at exe; ownedXDGFiles
// (their names alone) is what Uninstall removes.
func xdgFiles(exe string) ([]xdgFile, error) {
	entry, err := desktopEntry(exe)
	if err != nil {
		return nil, err
	}
	pngs, err := icoPNGs(assets.Icon)
	if err != nil {
		return nil, fmt.Errorf("the program icon: %w", err)
	}
	files := []xdgFile{
		{mimeRel, []byte(mimePackage())},
		{desktopRel, []byte(entry)},
	}
	for _, n := range iconSizes {
		p, ok := pngs[n]
		if !ok {
			return nil, fmt.Errorf("the program icon has no %d px image", n)
		}
		files = append(files, xdgFile{iconRel(n), p})
	}
	return files, nil
}

func ownedXDGFiles() []string {
	rels := []string{mimeRel, desktopRel}
	for _, n := range iconSizes {
		rels = append(rels, iconRel(n))
	}
	return rels
}

// dropDefault takes desktop file id out of the mime= lines of a mimeapps.list,
// in the sections that name an application for a type ([Default Applications],
// [Added Associations]): from a list of several, only id goes; a line left with
// none goes. It reports whether anything changed.
func dropDefault(content, mime, id string) (string, bool) {
	lines := strings.SplitAfter(content, "\n")
	var out []string
	section, changed := "", false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") {
			section = t
		}
		k, v, ok := strings.Cut(t, "=")
		if ok && strings.TrimSpace(k) == mime && (section == "[Default Applications]" || section == "[Added Associations]") {
			var keep []string
			dropped := false
			for _, a := range strings.Split(v, ";") {
				switch a = strings.TrimSpace(a); a {
				case id:
					dropped = true
				case "":
				default:
					keep = append(keep, a)
				}
			}
			if dropped {
				changed = true
				if len(keep) == 0 {
					continue
				}
				nl := ""
				if strings.HasSuffix(l, "\n") {
					nl = "\n"
				}
				l = mime + "=" + strings.Join(keep, ";") + ";" + nl
			}
		}
		out = append(out, l)
	}
	return strings.Join(out, ""), changed
}

// xdgDesktop registers the file type with a freedesktop.org desktop.
type xdgDesktop struct {
	getenv func(string) string // nil: os.Getenv
	// run runs one of the desktop's tools. A tool that is not installed is an
	// error when required, else it is skipped. nil: runTool.
	run func(required bool, name string, args ...string) error
}

func (x *xdgDesktop) env(k string) string {
	if x.getenv != nil {
		return x.getenv(k)
	}
	return os.Getenv(k)
}

func (x *xdgDesktop) tool(required bool, name string, args ...string) error {
	if x.run != nil {
		return x.run(required, name, args...)
	}
	return runTool(required, name, args...)
}

func runTool(required bool, name string, args ...string) error {
	if _, err := exec.LookPath(name); err != nil {
		if required {
			return fmt.Errorf("%s is not installed", name)
		}
		log.Printf("%s is not installed; skipped", name)
		return nil
	}
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		err = fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, bytes.TrimSpace(out))
		if required {
			return err
		}
		log.Print(err)
	}
	return nil
}

func (x *xdgDesktop) register(exe string) error {
	data, err := XDGDataHome(x.env)
	if err != nil {
		return err
	}
	files, err := xdgFiles(exe)
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := writeAtomic(filepath.Join(data, filepath.FromSlash(f.rel)), f.data); err != nil {
			return err
		}
	}
	if err := x.updateCaches(data); err != nil {
		return err
	}
	return x.tool(true, "xdg-mime", "default", desktopID, brand.MIMEType)
}

// updateCaches rebuilds what the desktop reads instead of the files themselves.
func (x *xdgDesktop) updateCaches(data string) error {
	var errs []error
	if mime := filepath.Join(data, "mime"); dirExists(filepath.Join(mime, "packages")) {
		errs = append(errs, x.tool(true, "update-mime-database", mime))
	}
	if apps := filepath.Join(data, "applications"); dirExists(apps) {
		errs = append(errs, x.tool(false, "update-desktop-database", apps))
	}
	// Without a cache the icon folders are read directly; a cache that is there must be renewed.
	if hicolor := filepath.Join(data, "icons", "hicolor"); fileExists(filepath.Join(hicolor, "icon-theme.cache")) {
		errs = append(errs, x.tool(false, "gtk-update-icon-cache", "-q", "-t", "-f", hicolor))
	}
	return errors.Join(errs...)
}

func (x *xdgDesktop) remove() error {
	data, err := XDGDataHome(x.env)
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range x.mimeappsLists(data) {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if s, changed := dropDefault(string(b), brand.MIMEType, desktopID); changed {
			if hasNoKeys(s) {
				errs = append(errs, os.Remove(p)) // xdg-mime made it for us; nothing else is in it
			} else {
				errs = append(errs, writeAtomic(p, []byte(s)))
			}
		}
	}
	for _, rel := range ownedXDGFiles() {
		p := filepath.Join(data, filepath.FromSlash(rel))
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	// Icon folders that are now empty were made by Install.
	for _, n := range iconSizes {
		for d := filepath.Dir(filepath.Join(data, filepath.FromSlash(iconRel(n)))); d != data; d = filepath.Dir(d) {
			if os.Remove(d) != nil {
				break // not empty, or not there
			}
		}
	}
	// The cache update-desktop-database made, when no desktop entry is left for it.
	if apps := filepath.Join(data, "applications"); onlyCache(apps, "mimeinfo.cache") {
		errs = append(errs, os.Remove(filepath.Join(apps, "mimeinfo.cache")))
		os.Remove(apps)
	}
	// The MIME folder is made from packages/ alone: with no package left, all of it goes.
	if mime := filepath.Join(data, "mime"); emptyDir(filepath.Join(mime, "packages")) {
		errs = append(errs, os.RemoveAll(mime))
	}
	errs = append(errs, x.updateCaches(data))
	return errors.Join(errs...)
}

// hasNoKeys says whether a mimeapps.list holds nothing but section headers,
// comments and blank lines.
func hasNoKeys(s string) bool {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "[") && !strings.HasPrefix(l, "#") {
			return false
		}
	}
	return true
}

// onlyCache says whether dir holds the file cache and nothing else.
func onlyCache(dir, cache string) bool {
	ents, err := os.ReadDir(dir)
	return err == nil && len(ents) == 1 && ents[0].Name() == cache
}

// mimeappsLists are the files in which xdg-mime, or a desktop's own settings,
// may have made the viewer the default: per desktop, then in general, then the
// old place in the data folder.
func (x *xdgDesktop) mimeappsLists(data string) []string {
	var lists []string
	if cfg, err := xdgConfigHome(x.env); err == nil {
		for _, d := range strings.Split(x.env("XDG_CURRENT_DESKTOP"), ":") {
			if d = strings.ToLower(strings.TrimSpace(d)); d != "" && !strings.ContainsAny(d, `/\`) {
				lists = append(lists, filepath.Join(cfg, d+"-mimeapps.list"))
			}
		}
		lists = append(lists, filepath.Join(cfg, "mimeapps.list"))
	}
	lists = append(lists, filepath.Join(data, "applications", "mimeapps.list"))
	return slices.Compact(lists)
}

// notify does nothing: register and remove renew the desktop's caches themselves.
func (x *xdgDesktop) notify() {}

// writeAtomic writes p by way of a file beside it, making its folder.
func writeAtomic(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".new"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
