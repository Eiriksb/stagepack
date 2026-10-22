package deck

import (
	"bytes"
	"encoding/json"
	"path"
	"regexp"
	"strings"
	"unicode"

	"stagepack/internal/brand"
	slidevrt "stagepack/runtime"
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

// CodeReasons lists why a deck's files make it contain code, one line per
// reason, in the order of names; nil if they don't. names are the deck's entry
// names (as in the zip; a trailing slash marks a folder), and read returns the
// content of an entry (nil if it can't). Only package.json files are read.
//
// It counts: config files Vite and UnoCSS load (vite.config*, uno.config*,
// unocss.config*, and PostCSS's); everything under setup/; any .vue, .js, .ts
// (and kin), .node or .wasm file, public/ included; each bundled package with
// such a file, or whose package.json lists addons (once, by name); and any
// package.json that lists addons, has a PostCSS config or can't be read.
// Packages of only CSS, fonts and data do not count.
//
// The theme and addons in the slides' headmatter are not looked at here: the
// viewer asks Slidev's own parser for them (slidev.ReadHeadmatter) and judges
// them with HeadmatterReasons. SlidesReasons is a rougher check for pack.
func CodeReasons(names []string, read func(name string) []byte) []string {
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
		}
	}
	return reasons
}

// packageJSONReasons says why a package.json makes a deck contain code: it
// can't be read, it has a PostCSS config, or it lists addons.
func packageJSONReasons(data []byte) []string {
	addons, postcss, ok := readPackageJSON(data)
	var why []string
	if !ok {
		why = append(why, "it can't be read, so its addons can't be checked")
	}
	if postcss {
		why = append(why, `"postcss": configuration that runs as code`)
	}
	if addons {
		why = append(why, "it lists Slidev addons, which can run code")
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

// readPackageJSON reports whether a package.json lists any Slidev addons in
// "slidev": {"addons": [...]}, and whether it has a PostCSS config in "postcss"
// (which may load a plugin from any file; Slidev 53 makes Vite skip it, but a
// later version might not). Keys are matched exactly, as JavaScript reads them.
// ok is false if the file is not JSON.
func readPackageJSON(data []byte) (addons, postcss, ok bool) {
	data = bytes.TrimPrefix(data, []byte(BOM))
	if len(bytes.TrimSpace(data)) == 0 {
		return false, false, true // nothing to read; JavaScript can't parse it either
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(data, &top) != nil {
		return false, false, false
	}
	_, postcss = top["postcss"]
	var slidev map[string]json.RawMessage
	var list []json.RawMessage
	if json.Unmarshal(top["slidev"], &slidev) == nil && json.Unmarshal(slidev["addons"], &list) == nil {
		addons = len(list) > 0
	}
	return addons, postcss, true
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

// HeadmatterReasons judges the theme and addons Slidev's parser read from the
// slides' headmatter (JSON values; null or missing when not set). A theme that
// isn't one the runtime bundles counts, and so do any addons: the runtime
// bundles none, so each comes from the talk, a folder or a bundled package.
// Each reason holds its value in full, one per addon: the reasons go into the
// version hash (workspace.Work.AddCode), so nothing may be cut from them here.
func HeadmatterReasons(theme, addons json.RawMessage) []string {
	var out []string
	var name string
	switch {
	case isNull(theme):
	case json.Unmarshal(theme, &name) == nil && (name == "" || bundledThemes[name]):
	default:
		out = append(out, "the theme "+string(bytes.TrimSpace(theme))+` (any theme but the bundled "default" can run code)`)
	}
	var list []json.RawMessage
	switch {
	case isNull(addons):
	case json.Unmarshal(addons, &list) != nil:
		out = append(out, "the addons "+string(bytes.TrimSpace(addons))+" (addons can run code)")
	default:
		for _, a := range list {
			out = append(out, "the addon "+string(bytes.TrimSpace(a))+" (addons can run code)")
		}
	}
	return out
}

func isNull(v json.RawMessage) bool {
	t := bytes.TrimSpace(v)
	return len(t) == 0 || string(t) == "null"
}

var (
	// themeKey is theme or addons followed by a colon, as a key may be written:
	// quoted or not, with blanks before the colon. Case matters, as in YAML.
	themeKey = regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])(theme|addons)["']?[ \t]*:`)
	// plainTheme is the one allowed form of a theme line; group 2 is the name.
	// A # starts a comment only after a blank: `default#x` is all one value.
	plainTheme = regexp.MustCompile(`^theme:[ \t]*(["']?)([A-Za-z0-9@/._-]+)(["']?)(?:[ \t]+#.*)?[ \t]*$`)
	// anchorOrAlias is a YAML anchor, alias or merge key, which can make a key
	// "theme" without spelling it: `x: &k theme` then `*k : ./folder`.
	anchorOrAlias = regexp.MustCompile(`(?:^|[\s{\[,:?-])[&*][^\s]|<<`)
	// escapedKey is a double-quoted key with an escape in it; explicitKey is
	// YAML's "? key" form, at the start of a line or in a flow collection.
	escapedKey  = regexp.MustCompile(`"(?:[^"\\]|\\.)*\\(?:[^"\\]|\\.)*"[ \t]*:`)
	explicitKey = regexp.MustCompile(`(?:^|[{\[,])[ \t]*\?(?:[ \t]|$)`)
	// lineBreaks are what YAML parsers may take as the end of a line.
	lineBreaks = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\u0085", "\n", "\u2028", "\n", "\u2029", "\n")
	// bundledThemes are the theme names the runtime resolves to its own packages.
	bundledThemes = func() map[string]bool {
		m := map[string]bool{"none": true}
		for pkg := range slidevrt.Lock() {
			if name, ok := strings.CutPrefix(pkg, "@slidev/theme-"); ok && !strings.Contains(name, "/") {
				m[name], m[pkg] = true, true
			}
		}
		return m
	}()
)

// SlidesReasons is a rough check, for pack's note, of whether a Markdown file
// sets a theme or addons that would count. The viewer does not use it: it asks
// Slidev's parser (see HeadmatterReasons). Slidev reads the headmatter with a
// full YAML parser, and YAML has many ways to spell a key or to carry a value
// over lines; this check allows one plain form and counts everything else:
//
//   - The only theme a talk may set without being asked about is one the
//     runtime bundles (or none), on a line of its own in the --- block at the
//     top of the slides file: `theme: default`. Nothing before it in the file
//     may open a { or [ collection, and the line after it must start a new key.
//   - Addons always count: the runtime bundles none, so each one comes from the
//     talk (a folder, or a bundled package).
//   - Any other line in any Markdown file that YAML could read as a theme: or
//     addons: key counts, and so does any quoted key with an escape in it (which
//     could spell "theme" without the letters) and any explicit "? key".
//
// So a talk about Slidev that shows `theme: ./my-theme` in a code block, or
// says "theme:" in its text, is noted too. isEntry is true for the slides file.
func SlidesReasons(name, text string, isEntry bool) []string {
	// Slidev splits lines on \n only, its YAML parser on \r too; other breaks
	// are split here so nothing hides behind them, but they rule out the plain form.
	odd := strings.ContainsAny(text, "\u0085\u2028\u2029")
	lines := strings.Split(lineBreaks.Replace(strings.TrimPrefix(text, BOM)), "\n")
	headEnd := 0 // lines[1:headEnd] are the --- block at the top, if any
	if isEntry && isFence(lines[0]) {
		for i := 1; i < len(lines); i++ {
			if isFence(lines[i]) {
				headEnd = i
				break
			}
		}
	}
	var out []string
	flow := false // a { or [ has been seen in the file so far
	for i, l := range lines {
		switch {
		case escapedKey.MatchString(l) || explicitKey.MatchString(l) || (i < headEnd && anchorOrAlias.MatchString(l)):
			out = append(out, "a YAML key in "+name+" (written in a way that can't be checked)")
		case themeKey.MatchString(l):
			m := plainTheme.FindStringSubmatch(l)
			if !odd && i > 0 && i < headEnd && !flow && m != nil && m[1] == m[3] && bundledThemes[m[2]] && newKeyFollows(lines[i+1:]) {
				break // theme: default
			}
			key := themeKey.FindStringSubmatch(l)[1]
			if key == "addons" {
				out = append(out, "addons set in "+name+" (addons can run code)")
			} else {
				out = append(out, "a theme set in "+name+" (any theme but the bundled \"default\" can run code)")
			}
		}
		flow = flow || strings.ContainsAny(l, "{[")
	}
	return out
}

// newKeyFollows reports whether the next line that holds something starts at
// the left edge with a key or is the closing ---: not a line YAML would add to
// the value before it.
func newKeyFollows(rest []string) bool {
	for _, sub := range rest {
		if strings.TrimSpace(sub) == "" {
			continue
		}
		if isFence(sub) {
			return true
		}
		c := sub[0]
		return c == '"' || c == '\'' || c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
	}
	return true
}
