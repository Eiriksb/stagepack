// Package pack turns a Slidev project folder into a deck file (brand.Ext).
package pack

import (
	"archive/zip"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"stagepack/internal/brand"
	"stagepack/internal/deck"
	"stagepack/runtime"
)

// Options says what to pack and where to put it.
type Options struct {
	Dir     string            // the Slidev project folder
	Out     string            // the deck file (brand.Ext) to write
	Entry   string            // slides file relative to Dir; default "slides.md"
	Title   string            // overrides the title from the entry's headmatter
	Exclude []string          // path prefixes (relative to Dir, whole components) left out
	Meta    map[string]string // free-form manifest metadata
	Warn    io.Writer         // notes and warnings; nil discards them
}

const defaultEntry = "slides.md"

// The bundled favicon is named after the brand: public/<brand>-favicon.svg, served as /<brand>-favicon.svg.
var (
	faviconName = strings.ToLower(brand.Name) + "-favicon.svg"
	faviconPath = "public/" + faviconName
	faviconURL  = "/" + faviconName
)

//go:embed favicon.svg
var faviconSVG []byte

// skipPackages are never vendored: the Slidev CLI is the runtime's, and the export tools need a browser.
var skipPackages = map[string]bool{"@slidev/cli": true, "playwright-chromium": true, "playwright": true}

// item is one file going into the zip: either a file on disk or in-memory data.
type item struct {
	name string // zip entry name, forward slashes
	src  string // path on disk; empty when data is used
	data []byte
	mod  time.Time
}

// Pack writes the project in o.Dir to o.Out and returns the manifest it wrote.
// Out is written to Out+".tmp" and renamed, so a failed pack leaves an existing Out untouched.
func Pack(o Options) (deck.Manifest, error) {
	warn := o.Warn
	if warn == nil {
		warn = io.Discard
	}
	if o.Dir == "" || o.Out == "" {
		return deck.Manifest{}, fmt.Errorf("pack: both a folder and an output file are required")
	}
	dir, err := filepath.Abs(o.Dir)
	if err != nil {
		return deck.Manifest{}, err
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return deck.Manifest{}, fmt.Errorf("pack: %s is not a folder", o.Dir)
	}
	out, err := filepath.Abs(o.Out)
	if err != nil {
		return deck.Manifest{}, err
	}
	entry := o.Entry
	if entry == "" {
		entry = defaultEntry
	}
	entry = path.Clean(filepath.ToSlash(entry))
	if err := deck.CheckSlidesEntry(entry); err != nil {
		return deck.Manifest{}, fmt.Errorf("pack: %w", err)
	}

	pj, err := readPackageJSON(filepath.Join(dir, "package.json"))
	if err != nil {
		return deck.Manifest{}, fmt.Errorf("pack: %w (is %s a Slidev project?)", err, o.Dir)
	}
	slidev, err := checkSlidev(dir, pj, warn)
	if err != nil {
		return deck.Manifest{}, err
	}

	items, err := collectProject(dir, out, o.Exclude)
	if err != nil {
		return deck.Manifest{}, err
	}
	entryIdx := -1
	for i, it := range items {
		if it.name == entry {
			entryIdx = i
		}
	}
	if entryIdx < 0 {
		return deck.Manifest{}, fmt.Errorf("pack: entry %s not found in %s", entry, o.Dir)
	}
	entryText, err := os.ReadFile(items[entryIdx].src)
	if err != nil {
		return deck.Manifest{}, err
	}

	title := o.Title
	if title == "" {
		title = deck.HeadmatterValue(string(entryText), "title")
	}
	if title == "" {
		title = filepath.Base(dir)
	}

	// The author's file is never touched; the packed copy gets an offline favicon if it needs one.
	if packed, changed := withLocalFavicon(string(entryText)); changed {
		items[entryIdx].data = []byte(packed)
		items[entryIdx].src = ""
		if !hasItem(items, faviconPath) {
			items = append(items, item{name: faviconPath, data: faviconSVG, mod: time.Now()})
		}
		fmt.Fprintf(warn, "note: no local favicon in %s; using the bundled %s so the viewer makes no network request\n", entry, faviconURL)
	}
	if !fontsOffline(string(entryText)) {
		fmt.Fprintf(warn, "warning: %s does not set fonts: provider: none, so Slidev loads the talk's fonts from "+
			"Google Fonts when it is shown, which needs the internet; to work offline, bundle the fonts (Fontsource "+
			"packages) and set fonts: provider: none in the headmatter\n", entry)
	}

	vendored, err := vendor(dir, pj)
	if err != nil {
		return deck.Manifest{}, err
	}
	items = append(items, vendored...)

	// Fail now, naming the entry, rather than packing a deck that cannot be opened.
	names := []string{brand.ManifestName}
	for _, it := range items {
		names = append(names, it.name)
	}
	if _, _, err := deck.CheckEntryNames(names); err != nil {
		return deck.Manifest{}, fmt.Errorf("pack: %w", err)
	}
	// The same rules the viewer uses to decide whether to ask before showing the talk.
	byName := map[string]item{}
	for _, it := range items {
		byName[it.name] = it
	}
	read := func(name string) []byte {
		it, ok := byName[name]
		if !ok || it.src == "" {
			return it.data
		}
		data, _ := os.ReadFile(it.src)
		return data
	}
	reasons := deck.CodeReasons(names, read)
	for _, n := range names {
		if strings.EqualFold(path.Ext(n), ".md") {
			reasons = append(reasons, deck.SlidesReasons(n, string(read(n)), n == entry)...)
		}
	}
	if len(reasons) > 0 {
		fmt.Fprintf(warn, "note: this talk contains code, which runs on the presenter's computer, so the viewer "+
			"asks before showing it (once for each version of the talk). It contains code because of (theme and "+
			"addons lines are a rough check; the viewer asks Slidev's own parser):\n  %s\n",
			strings.Join(reasons, "\n  "))
	}

	id, err := newID()
	if err != nil {
		return deck.Manifest{}, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	meta := o.Meta
	if meta == nil {
		meta = map[string]string{}
	}
	m := deck.Manifest{
		Format: deck.FormatVersion, ID: id, Title: title, Created: now, Updated: now,
		Slidev: slidev, Entry: entry, Meta: meta,
	}
	if err := writeZip(out, m, items); err != nil {
		return deck.Manifest{}, err
	}
	return m, nil
}

func hasItem(items []item, name string) bool {
	for _, it := range items {
		if it.name == name {
			return true
		}
	}
	return false
}

// checkSlidev returns the Slidev version the project uses. A different major from the runtime's is an
// error; a different minor or patch is a warning.
func checkSlidev(dir string, pj packageJSON, warn io.Writer) (string, error) {
	version := ""
	if inst, err := readPackageJSON(filepath.Join(dir, "node_modules", "@slidev", "cli", "package.json")); err == nil {
		version = inst.Version
	}
	if version == "" {
		declared := pj.Dependencies["@slidev/cli"]
		if declared == "" {
			declared = pj.DevDependencies["@slidev/cli"]
		}
		if declared == "" {
			return "", fmt.Errorf("pack: @slidev/cli is not in package.json; this is not a Slidev project")
		}
		version = strings.TrimLeft(strings.TrimSpace(declared), "^~=v")
		fmt.Fprintf(warn, "warning: @slidev/cli is not installed in node_modules; taking its version %s from package.json\n", version)
	}
	got, err := deck.Major(version)
	if err != nil {
		return "", fmt.Errorf("pack: Slidev version %q: %w", version, err)
	}
	if got != runtime.Major() {
		return "", fmt.Errorf("pack: the talk uses Slidev %d (%s), but the runtime has %d (%s); "+
			"use the %s build that bundles Slidev %d or move the talk to Slidev %d",
			got, version, runtime.Major(), runtime.SlidevVersion(), brand.Name, got, runtime.Major())
	}
	if version != runtime.SlidevVersion() {
		fmt.Fprintf(warn, "warning: the talk was made with Slidev %s, the viewer runs %s; check it in the viewer\n",
			version, runtime.SlidevVersion())
	}
	return version, nil
}

// collectProject lists the project's files: everything except node_modules, .git, a top-level dist and
// .slidev, the excluded prefixes and the output file. Symlinks are followed. .gitignore is not honoured.
func collectProject(dir, out string, exclude []string) ([]item, error) {
	var ex []string
	for _, e := range exclude {
		e = strings.Trim(path.Clean(filepath.ToSlash(e)), "/")
		if e != "" && e != "." {
			ex = append(ex, e)
		}
	}
	excluded := func(rel string) bool {
		for _, e := range ex {
			if rel == e || strings.HasPrefix(rel, e+"/") {
				return true
			}
		}
		return false
	}
	var items []item
	var walk func(abs, rel string, ancestors []string) error
	walk = func(abs, rel string, ancestors []string) error {
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return fmt.Errorf("pack: %s: %w", rel, err)
		}
		for _, a := range ancestors {
			if a == real {
				return fmt.Errorf("pack: %s links back to a folder that contains it", rel)
			}
		}
		ancestors = append(ancestors, real)
		des, err := os.ReadDir(abs)
		if err != nil {
			return err
		}
		for _, de := range des {
			name := de.Name()
			childRel := path.Join(rel, name)
			if rel == "" {
				childRel = name
			}
			childAbs := filepath.Join(abs, name)
			st, err := os.Stat(childAbs) // follows symlinks
			if err != nil {
				return fmt.Errorf("pack: %s: %w", childRel, err)
			}
			if st.IsDir() {
				switch {
				case name == "node_modules" || name == ".git":
					continue
				case rel == "" && (name == "dist" || name == ".slidev"):
					continue
				case excluded(childRel):
					continue
				}
				if err := walk(childAbs, childRel, ancestors); err != nil {
					return err
				}
				continue
			}
			if !st.Mode().IsRegular() || excluded(childRel) || childAbs == out || childAbs == out+".tmp" {
				continue
			}
			items = append(items, item{name: childRel, src: childAbs, mod: st.ModTime()})
		}
		return nil
	}
	if err := walk(dir, "", nil); err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].name < items[j].name })
	return items, nil
}

// writeZip writes the manifest and items to out+".tmp" and renames it to out.
func writeZip(out string, m deck.Manifest, items []item) (err error) {
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	tmp := out + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(tmp)
		}
	}()
	zw := zip.NewWriter(f)

	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := addEntry(zw, item{name: brand.ManifestName, data: append(manifest, '\n'), mod: m.Created}); err != nil {
		return err
	}
	for _, it := range items {
		if err := addEntry(zw, it); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, out)
}

func addEntry(zw *zip.Writer, it item) error {
	w, err := zw.CreateHeader(&zip.FileHeader{Name: it.name, Method: deck.EntryMethod(it.name), Modified: it.mod})
	if err != nil {
		return err
	}
	if it.src == "" {
		_, err = w.Write(it.data)
		return err
	}
	src, err := os.Open(it.src)
	if err != nil {
		return err
	}
	defer src.Close()
	if _, err := io.Copy(w, src); err != nil {
		return fmt.Errorf("pack: %s: %w", it.name, err)
	}
	return nil
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

// fontsOffline reports whether the headmatter sets fonts.provider to none, either as
//
//	fonts:
//	  provider: none
//
// or as `fonts: { provider: none }`. Slidev's default provider is Google Fonts, which the
// browser loads from the internet when the talk is shown.
func fontsOffline(text string) bool {
	lines, _ := deck.Headmatter(text)
	for i, l := range lines {
		v, found := deck.KeyValue(l, "fonts")
		if !found {
			continue
		}
		if strings.HasPrefix(v, "{") {
			for _, part := range strings.Split(strings.Trim(v, "{} "), ",") {
				if pv, ok := deck.KeyValue(strings.TrimSpace(part), "provider"); ok && pv == "none" {
					return true
				}
			}
			return false
		}
		for _, sub := range lines[i+1:] {
			trimmed := strings.TrimSpace(sub)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			if sub[0] != ' ' && sub[0] != '\t' {
				break // the next top-level key
			}
			if pv, ok := deck.KeyValue(trimmed, "provider"); ok {
				return pv == "none"
			}
		}
		return false
	}
	return false
}

// remoteURL reports whether a favicon value makes the browser fetch it from another host.
func remoteURL(v string) bool {
	return strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") || strings.HasPrefix(v, "//")
}

// withLocalFavicon returns text with the headmatter favicon set to the bundled local one if the deck
// has none, an empty one or a remote one (Slidev's default is a CDN URL, a network request at run
// time). changed is false when the deck has a local favicon. A byte order mark and the line endings
// are kept.
func withLocalFavicon(text string) (packed string, changed bool) {
	mark := ""
	if strings.HasPrefix(text, deck.BOM) {
		mark, text = deck.BOM, strings.TrimPrefix(text, deck.BOM)
	}
	nl := "\n"
	if strings.Contains(text, "\r\n") {
		nl = "\r\n"
	}
	line := "favicon: " + faviconURL
	lines, ok := deck.Headmatter(text)
	if !ok {
		return mark + "---" + nl + line + nl + "---" + nl + text, true
	}
	for i, l := range lines {
		v, found := deck.KeyValue(l, "favicon")
		if !found {
			continue
		}
		if v != "" && !remoteURL(v) {
			return mark + text, false
		}
		all := strings.Split(text, "\n")
		cr := ""
		if strings.HasSuffix(all[1+i], "\r") {
			cr = "\r"
		}
		all[1+i] = line + cr
		return mark + strings.Join(all, "\n"), true
	}
	all := strings.Split(text, "\n")
	cr := ""
	if strings.HasSuffix(all[0], "\r") {
		cr = "\r"
	}
	rest := append([]string{all[0], line + cr}, all[1:]...)
	return mark + strings.Join(rest, "\n"), true
}
