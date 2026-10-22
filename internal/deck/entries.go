package deck

import (
	"archive/zip"
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// The rules for entry names inside a deck file. Pack and the extractor
// both use them, so a deck that packs always opens: the names are legal on
// Windows too, whichever OS made the deck.

const (
	vendorPrefix = "vendor/node_modules/"
	nodeModules  = "node_modules/"
)

// ExtractedPath maps a zip entry name (as returned by CleanEntryName, without
// a trailing slash) to where it lands in the work folder: vendor/node_modules/*
// goes to node_modules/*.
func ExtractedPath(rel string) string {
	if rest, ok := strings.CutPrefix(rel+"/", vendorPrefix); ok {
		return strings.TrimSuffix(nodeModules+rest, "/")
	}
	return rel
}

// reserved Windows device names (with or without an extension).
var reserved = map[string]bool{"con": true, "prn": true, "aux": true, "nul": true}

func reservedName(seg string) bool {
	base := seg
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	base = strings.ToLower(strings.TrimRight(base, " "))
	if reserved[base] {
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "com") || strings.HasPrefix(base, "lpt")) && base[3] >= '1' && base[3] <= '9'
}

// CleanEntryName checks one entry name and returns it as a slash-separated
// relative path. A trailing slash marks a folder. It rejects anything that
// could leave the work folder on any OS, or that Windows cannot store: a
// backslash, a drive letter or other ':', any of <>"|?*, control characters,
// absolute paths, '.' and '..' and empty segments, a segment ending in a dot
// or space, and device names such as CON or aux.txt.
func CleanEntryName(name string) (rel string, isDir bool, err error) {
	bad := func(why string) (string, bool, error) {
		return "", false, fmt.Errorf("entry %q: %s", name, why)
	}
	if name == "" {
		return bad("empty name")
	}
	if strings.ContainsAny(name, "\\:<>\"|?*") {
		return bad(`contains one of \ : < > " | ? *, which Windows cannot store`)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return bad("control character")
		}
	}
	if strings.HasPrefix(name, "/") {
		return bad("absolute path")
	}
	rel = name
	if strings.HasSuffix(rel, "/") {
		isDir = true
		rel = strings.TrimSuffix(rel, "/")
	}
	for _, seg := range strings.Split(rel, "/") {
		switch {
		case seg == "":
			return bad("empty path segment")
		case seg == "." || seg == "..":
			return bad("'.' or '..' segment")
		case strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " "):
			return bad("a name ends with a dot or space, which Windows cannot store")
		case reservedName(seg):
			return bad("a name is reserved on Windows")
		}
	}
	if !filepath.IsLocal(filepath.FromSlash(rel)) {
		return bad("path is not local")
	}
	return rel, isDir, nil
}

// CheckEntryName reports why name cannot be an entry of a deck, or nil.
func CheckEntryName(name string) error {
	_, _, err := CleanEntryName(name)
	return err
}

// CheckEntryNames checks every name, then the set: no two entries may land on
// the same path in the work folder once the vendor/node_modules mapping and
// case-folding (Windows) are applied, and no path may be both a file and a
// folder. It returns the cleaned, mapped paths (parallel to names) and
// whether each is a folder.
func CheckEntryNames(names []string) (paths []string, isDir []bool, err error) {
	paths = make([]string, len(names))
	isDir = make([]bool, len(names))
	kind := map[string]bool{} // lower-cased path -> is a folder
	set := func(p string, dir bool, orig string) error {
		key := strings.ToLower(p)
		if prev, ok := kind[key]; ok {
			if prev != dir {
				return fmt.Errorf("entry %q is both a file and a folder", orig)
			}
			if !dir {
				return fmt.Errorf("duplicate entry %q (entries that differ only by case count as the same on Windows)", orig)
			}
		}
		kind[key] = dir
		return nil
	}
	for i, name := range names {
		rel, dir, err := CleanEntryName(name)
		if err != nil {
			return nil, nil, err
		}
		rel = ExtractedPath(rel)
		for j := 0; j < len(rel); j++ {
			if rel[j] == '/' {
				if err := set(rel[:j], true, name); err != nil {
					return nil, nil, err
				}
			}
		}
		if err := set(rel, dir, name); err != nil {
			return nil, nil, err
		}
		paths[i], isDir[i] = rel, dir
	}
	return paths, isDir, nil
}

// CheckSlidesEntry reports why name cannot be a deck's slides file (the
// manifest's "entry"), or nil. It is a plain file name at the top of the deck,
// ending in .md: Slidev gets it on its command line and the viewer joins it to
// the work folder, so it must not start with '-' or hold a '/'.
func CheckSlidesEntry(name string) error {
	if err := CheckEntryName(name); err != nil {
		return fmt.Errorf("slides %w", err)
	}
	switch {
	case strings.Contains(name, "/"):
		return fmt.Errorf("slides entry %q: must be at the top of the deck, not in a folder", name)
	case strings.HasPrefix(name, "-"):
		return fmt.Errorf("slides entry %q: must not start with '-'", name)
	case !strings.EqualFold(path.Ext(name), ".md"):
		return fmt.Errorf("slides entry %q: must end in .md", name)
	}
	return nil
}

// textExts are deflated; everything else is stored.
var textExts = map[string]bool{
	".md": true, ".vue": true, ".ts": true, ".js": true, ".mjs": true, ".cjs": true, ".css": true,
	".json": true, ".html": true, ".svg": true, ".txt": true, ".yaml": true, ".yml": true, ".map": true,
}

// EntryMethod is how an entry is compressed: text (by extension) is deflated,
// everything else (media, fonts, archives) is stored.
func EntryMethod(name string) uint16 {
	if textExts[strings.ToLower(path.Ext(name))] {
		return zip.Deflate
	}
	return zip.Store
}
