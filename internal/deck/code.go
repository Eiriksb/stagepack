package deck

import (
	"encoding/json"
	"path"
	"strings"

	"stagepack/internal/brand"
)

// A deck "contains code" when opening it runs something the talk brought along:
// Slidev and Vite load config files, setup scripts, local themes and addons,
// Vue components and packages from the talk itself, and these run as the
// person who opened it. The viewer asks before it starts such a deck.

// codeExts are the extensions of files Node or the browser may run.
var codeExts = map[string]bool{
	".vue": true, ".js": true, ".mjs": true, ".cjs": true, ".ts": true, ".mts": true, ".cts": true,
	".jsx": true, ".tsx": true, ".node": true, ".wasm": true,
}

// configPrefixes are config files Vite, UnoCSS and PostCSS load from the
// talk's folder, whatever their extension.
var configPrefixes = []string{"vite.config.", "uno.config.", "unocss.config.", "postcss.config.", ".postcssrc"}

// CodeReasons lists why a deck contains code, one line per reason, in the
// order of names; nil if it contains none. names are the deck's entry names
// (as in the zip; a trailing slash marks a folder), entry is its slides file,
// and read returns the content of an entry (nil if it can't). Only the slides
// file and package.json files are read.
//
// It counts: vite.config.*, uno.config.*, unocss.config.*, postcss.config.*
// and .postcssrc* at the top; everything under setup/; any .vue, .js, .ts
// (and kin), .node or .wasm file outside public/; each bundled package with
// such a file, or whose package.json names an addon by path (once, by name);
// and a theme or addon given as a path in the slides' headmatter or in the
// talk's package.json ("slidev": {"addons": [...]}). Packages of only CSS,
// fonts and data do not count, and neither does public/, which Slidev serves
// as plain files.
func CodeReasons(names []string, entry string, read func(name string) []byte) []string {
	var reasons []string
	seenPkg := map[string]bool{}
	pkgReason := func(pkg string) {
		if !seenPkg[strings.ToLower(pkg)] {
			seenPkg[strings.ToLower(pkg)] = true
			reasons = append(reasons, "package "+pkg+" (bundled with the talk)")
		}
	}
	for _, name := range names {
		if strings.HasSuffix(name, "/") || name == brand.ManifestName {
			continue
		}
		low := strings.ToLower(name) // Windows finds Vite.Config.TS as vite.config.ts
		if pkg, ok := packageOf(name); ok {
			if codeExts[path.Ext(low)] || (path.Base(name) == "package.json" && len(pathAddons(read(name))) > 0) {
				pkgReason(pkg)
			}
			continue
		}
		switch {
		case strings.HasPrefix(low, "public/"):
		case !strings.Contains(low, "/") && hasAnyPrefix(low, configPrefixes):
			reasons = append(reasons, name+" (configuration that runs as code)")
		case strings.HasPrefix(low, "setup/"):
			reasons = append(reasons, name+" (setup code)")
		case codeExts[path.Ext(low)]:
			reasons = append(reasons, name)
		}
	}
	reasons = append(reasons, localThemes(string(read(entry)))...)
	for _, a := range pathAddons(read("package.json")) {
		reasons = append(reasons, "addon "+a+" (loaded from a folder, named in package.json)")
	}
	return reasons
}

// pathAddons lists the addons a package.json names by path in "slidev": {"addons": [...]}.
func pathAddons(packageJSON []byte) []string {
	var pj struct {
		Slidev struct {
			Addons []any `json:"addons"`
		} `json:"slidev"`
	}
	if len(packageJSON) == 0 || json.Unmarshal(packageJSON, &pj) != nil {
		return nil
	}
	var out []string
	for _, a := range pj.Slidev.Addons {
		if s, ok := a.(string); ok && isPath(s) {
			out = append(out, s)
		}
	}
	return out
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// packageOf returns the package a vendor/node_modules/ (or node_modules/)
// entry belongs to: its top-level package, scoped names included.
func packageOf(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, vendorPrefix)
	if !ok {
		rest, ok = strings.CutPrefix(name, nodeModules)
	}
	if !ok {
		return "", false
	}
	segs := strings.SplitN(rest, "/", 3)
	if strings.HasPrefix(segs[0], "@") && len(segs) > 1 {
		return segs[0] + "/" + segs[1], true
	}
	return segs[0], true
}

// localThemes lists the theme and addons the headmatter gives as a path
// (./my-theme, themes/x, @/x, /abs, C:\x): Slidev loads their code from there.
// Names of packages (default, seriph, @org/addon) are not paths.
func localThemes(slides string) []string {
	lines, ok := Headmatter(slides)
	if !ok {
		return nil
	}
	var out []string
	for i, l := range lines {
		if v, found := KeyValue(l, "theme"); found && isPath(v) {
			out = append(out, "theme "+v+" (loaded from a folder)")
		}
		v, found := KeyValue(l, "addons")
		if !found {
			continue
		}
		var items []string
		if strings.HasPrefix(v, "[") {
			for _, part := range strings.Split(strings.Trim(v, "[] "), ",") {
				items = append(items, plainValue(part))
			}
		} else {
			for _, sub := range lines[i+1:] {
				t := strings.TrimSpace(sub)
				if t == "" || strings.HasPrefix(t, "#") {
					continue
				}
				if sub[0] != ' ' && sub[0] != '\t' && sub[0] != '-' {
					break // the next top-level key
				}
				if item, ok := strings.CutPrefix(t, "-"); ok {
					items = append(items, plainValue(item))
				}
			}
		}
		for _, it := range items {
			if isPath(it) {
				out = append(out, "addon "+it+" (loaded from a folder)")
			}
		}
	}
	return out
}

// isPath reports whether a theme or addon name is a path rather than a package
// name. Slidev's own rule: it starts with / or . or @/, or it has a / and no
// leading @. Backslashes, ~, file: and drive letters are counted too, to be safe.
func isPath(v string) bool {
	switch {
	case v == "":
		return false
	case strings.HasPrefix(v, "@/"), v[0] != '@' && strings.Contains(v, "/"):
		return true
	case strings.ContainsAny(v, "\\~:"), v[0] == '.':
		return true
	}
	return false
}
