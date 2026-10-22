package deck

import (
	"bytes"
	"encoding/json"
	"path"
	"regexp"
	"strings"
	"unicode"

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

// configPrefixes are config files Vite and UnoCSS load from the talk's
// folder, with any extension or none (unconfig loads an extensionless
// uno.config). PostCSS configs are counted too, though Slidev passes Vite its
// own PostCSS settings, so Vite does not look for them.
var configPrefixes = []string{"vite.config", "uno.config", "unocss.config", "postcss.config", ".postcssrc"}

// CodeReasons lists why a deck contains code, one line per reason, in the
// order of names; nil if it contains none. names are the deck's entry names
// (as in the zip; a trailing slash marks a folder), entry is its slides file,
// and read returns the content of an entry (nil if it can't). Only Markdown
// and package.json files are read.
//
// It counts: config files Vite and UnoCSS load (vite.config*, uno.config*,
// unocss.config*, and PostCSS's); everything under setup/; any .vue, .js, .ts
// (and kin), .node or .wasm file, public/ included; each bundled package with
// such a file, or whose package.json names an addon by path (once, by name);
// a theme or addon given as a path in any Markdown file or in the talk's
// package.json ("slidev": {"addons": [...]}); and any of these written in a
// way this check can't be sure of. Packages of only CSS, fonts and data do
// not count.
//
// Where Slidev reads a theme or addons with a full YAML parser, this check
// does not parse: it looks at every line of every Markdown file, and counts
// as code whatever is not plainly a package name (see markdownReasons). It
// errs towards asking.
func CodeReasons(names []string, entry string, read func(name string) []byte) []string {
	var reasons []string
	seenPkg := map[string]bool{}
	pkgReason := func(pkg string) {
		if !seenPkg[strings.ToLower(pkg)] {
			seenPkg[strings.ToLower(pkg)] = true
			reasons = append(reasons, "package "+pkg+" (bundled with the talk)")
		}
	}
	var markdown []string
	for _, name := range names {
		if strings.HasSuffix(name, "/") || name == brand.ManifestName {
			continue
		}
		// Compared folded: Windows finds Vite.Config.TS as vite.config.ts and
		// Node_Modules/x/Package.json as node_modules/x/package.json.
		low := fold(name)
		pkg, inPkg := packageOf(low)
		if path.Base(low) == "package.json" {
			// Every one of them: the talk's, a bundled package's, a theme folder's.
			for _, why := range packageJSONReasons(read(name)) {
				if inPkg {
					pkgReason(pkg)
				} else {
					reasons = append(reasons, name+" ("+why+")")
				}
			}
			continue
		}
		if inPkg {
			if codeExts[path.Ext(low)] || hasAnyPrefix(path.Base(low), configPrefixes) {
				pkgReason(pkg)
			}
			continue
		}
		switch {
		case hasAnyPrefix(path.Base(low), configPrefixes):
			reasons = append(reasons, name+" (configuration that runs as code)")
		case strings.HasPrefix(low, "setup/"):
			reasons = append(reasons, name+" (setup code)")
		case codeExts[path.Ext(low)]:
			reasons = append(reasons, name)
		case path.Ext(low) == ".md" || path.Ext(low) == ".markdown":
			markdown = append(markdown, name)
		}
	}
	for _, name := range markdown {
		reasons = append(reasons, markdownReasons(name, string(read(name)))...)
	}
	return reasons
}

// packageJSONReasons says why a package.json makes a deck contain code: it
// can't be read, it has a PostCSS config, or it names an addon by path.
func packageJSONReasons(data []byte) []string {
	paths, postcss, ok := pathAddons(data)
	var why []string
	if !ok {
		why = append(why, "it can't be read, so its addons can't be checked")
	}
	if postcss {
		why = append(why, `"postcss": configuration that runs as code`)
	}
	for _, a := range paths {
		why = append(why, "names addon "+a+", loaded from a folder")
	}
	return why
}

// fold is s as a case-insensitive file system may see it: each letter as the
// lower case of the smallest letter it folds with (ſ as s, the Kelvin sign as
// k). It may make more names alike than Windows does, which only asks more.
func fold(s string) string {
	return strings.Map(func(r rune) rune {
		least := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			least = min(least, f)
		}
		return unicode.ToLower(least)
	}, s)
}

// pathAddons lists the addons a package.json names in "slidev": {"addons": [...]}
// that are not plain package names, and a PostCSS config in "postcss" (which
// may load a plugin from any file; Slidev 53 makes Vite skip it, but a later
// version might not). Keys are matched exactly, as JavaScript reads them. ok
// is false if the file is not JSON.
func pathAddons(data []byte) (paths []string, postcss, ok bool) {
	data = bytes.TrimPrefix(data, []byte(BOM))
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, false, true // nothing to read; JavaScript can't parse it either
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(data, &top) != nil {
		return nil, false, false
	}
	_, postcss = top["postcss"]
	var slidev map[string]json.RawMessage
	var addons []json.RawMessage
	if json.Unmarshal(top["slidev"], &slidev) != nil || json.Unmarshal(slidev["addons"], &addons) != nil {
		return nil, postcss, true // not an object, not an array: Slidev reads no addons from it
	}
	for _, raw := range addons {
		var a string
		if json.Unmarshal(raw, &a) == nil && !packageName.MatchString(a) {
			paths = append(paths, a)
		}
	}
	return paths, postcss, true
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
// entry belongs to: its top-level package, scoped names included. name is folded.
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

var (
	// packageName is a plain npm package name (default, seriph, @org/addon):
	// Slidev looks it up in node_modules, where any code it has counts already.
	// Anything else may be a folder (./x, themes/x, @/x, /abs, C:\x).
	packageName = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$`)
	// themeKey finds a theme: or addons: key wherever YAML could read one: at the
	// start of a line (after indentation, "- ", "? ", tags and anchors), or
	// after { [ or , in a flow collection. Quoted keys count. Group 1 is the
	// indentation, 2 the key, 3 what follows the colon.
	themeKey = regexp.MustCompile(`(?:^([ \t]*)(?:[-?][ \t]+)*|[{\[,][ \t]*)(?:[!&][^ \t]*[ \t]+)*["']?(theme|addons)["']?[ \t]*:(.*)$`)
	// escapedKey is a double-quoted key with an escape in it, which could spell
	// "theme" without the letters; explicitKey is YAML's "? key" form, at the
	// start of a line or in a flow collection.
	escapedKey  = regexp.MustCompile(`"(?:[^"\\]|\\.)*\\(?:[^"\\]|\\.)*"[ \t]*:`)
	explicitKey = regexp.MustCompile(`(?:^|[{\[,])[ \t]*\?(?:[ \t]|$)`)
	// lineBreaks are what YAML parsers may take as the end of a line.
	lineBreaks = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\u0085", "\n", "\u2028", "\n", "\u2029", "\n")
)

// markdownReasons lists the themes and addons a Markdown file may set that are
// not plain package names. Slidev reads them from the slides' headmatter with
// a YAML parser; rather than guess where the headmatter ends or how a parser
// reads an odd line, every line of the file is looked at, and anything this
// check can't read plainly counts: a value that is not a package name, a value
// that may go on to the next line, a quoted key with escapes, an explicit
// "? key". A talk about Slidev that shows such a line in a code block is asked
// about too.
func markdownReasons(name, text string) []string {
	lines := strings.Split(lineBreaks.Replace(strings.TrimPrefix(text, BOM)), "\n")
	var out []string
	unclear := func(what string) {
		out = append(out, what+" in "+name+" (written in a way that can't be checked)")
	}
	for i, l := range lines {
		if escapedKey.MatchString(l) || explicitKey.MatchString(l) {
			unclear("a YAML key")
			continue
		}
		ix := themeKey.FindStringSubmatchIndex(l)
		if ix == nil {
			continue
		}
		key, v := l[ix[4]:ix[5]], yamlValue(l[ix[6]:ix[7]])
		if ix[2] < 0 {
			// In a flow collection ({…} or […]) a value may go on over lines at
			// any indentation: never plain enough.
			unclear(key)
			continue
		}
		indent := ix[3] - ix[2]
		var items []string
		switch {
		case key == "addons" && strings.HasPrefix(v, "["):
			if !strings.HasSuffix(v, "]") || strings.ContainsAny(v[1:len(v)-1], "[]{}") {
				unclear("addons")
				continue
			}
			for _, it := range strings.Split(v[1:len(v)-1], ",") {
				if it = strings.TrimSpace(it); it != "" {
					items = append(items, it)
				}
			}
		case key == "addons" && v == "":
			// A block list: "- name" lines, until a line no deeper than the key.
			for _, sub := range lines[i+1:] {
				t := strings.TrimLeft(sub, " \t")
				if t == "" || strings.HasPrefix(t, "#") {
					continue
				}
				if docEnd := strings.TrimRight(sub, " \t"); docEnd == "---" || docEnd == "..." {
					break // the end of the headmatter
				}
				d := len(sub) - len(t)
				if d < indent || (d == indent && !strings.HasPrefix(t, "-")) {
					break
				}
				item, ok := strings.CutPrefix(t, "-")
				if !ok || (item != "" && item[0] != ' ' && item[0] != '\t') {
					items = append(items, "\x00") // not a plain list item: can't be checked
					break
				}
				items = append(items, yamlValue(item))
			}
		default:
			if continues(lines[i+1:], indent) || listFollows(lines[i+1:], indent) {
				unclear(key) // a value that may go on, or a nested one
				continue
			}
			if v == "" {
				continue // null: Slidev's default
			}
			items = []string{v}
		}
		for _, it := range items {
			it = unquote(it)
			switch {
			case packageName.MatchString(it):
			case it != "" && isPath(it):
				out = append(out, strings.TrimSuffix(key, "s")+" "+it+" in "+name+" (loaded from a folder)")
			default:
				unclear(key)
			}
		}
	}
	return out
}

// yamlValue is v without a trailing # comment and surrounding blanks.
func yamlValue(v string) string {
	for i := 1; i < len(v); i++ {
		if v[i] == '#' && (v[i-1] == ' ' || v[i-1] == '\t') {
			v = v[:i]
			break
		}
	}
	return strings.TrimSpace(v)
}

// unquote strips one pair of matching quotes from a value without escapes;
// a value with escapes is returned as it is, which no package name matches.
func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		inner := v[1 : len(v)-1]
		if !strings.ContainsAny(inner, "\\'\"") {
			return inner
		}
	}
	return v
}

// continues reports whether the next line that holds something is deeper than
// indent, so that YAML may read it as part of the value on the line before.
func continues(rest []string, indent int) bool {
	for _, sub := range rest {
		t := strings.TrimLeft(sub, " \t")
		if t == "" {
			continue
		}
		return len(sub)-len(t) > indent
	}
	return false
}

// listFollows reports whether the next line that holds something is a "- "
// item at indent: the value is a list.
func listFollows(rest []string, indent int) bool {
	for _, sub := range rest {
		t := strings.TrimLeft(sub, " \t")
		if t == "" {
			continue
		}
		return len(sub)-len(t) == indent && strings.HasPrefix(t, "-") && strings.TrimRight(sub, " \t") != "---"
	}
	return false
}

// isPath reports whether a theme or addon name is a path. Slidev's own rule:
// it starts with / or . or @/, or it has a / and no leading @. Backslashes, ~
// and : (drive letters, file:) are counted too, to be safe.
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
