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
// (and kin), .node or .wasm file, public/ included; the talk's own index.html
// at the top, which Slidev puts into the page, scripts and all (with or
// without a doctype, though Slidev skips one with); each bundled package with
// such a file, or whose package.json lists addons (once, by name), and each
// bundled package with the export addon's name in its own (shadowsAddon), or
// that Slidev or Vite would load instead of the runtime's own or from the talk
// (shadowsRuntime), whatever it holds; and any package.json that lists addons,
// has a PostCSS config or can't be read. Other packages of only CSS, fonts and
// data do not count. The export addon (brand.AddonPkg), which the runtime
// bundles, doesn't count as an addon anywhere.
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
		if inPkg && (shadowsAddon(pkg) || shadowsRuntime(pkg)) {
			pkgReason(pkg)
			continue
		}
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
		case low == "index.html":
			// The slides entry is at the top (CheckSlidesEntry), so this is
			// Slidev's userRoot/index.html.
			reasons = append(reasons, name+" (Slidev puts it into the page, scripts and all)")
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
		for _, a := range list {
			addons = addons || !isBundledAddon(a)
		}
	}
	return addons, postcss, true
}

// bundledAddons are the names in addons: that Slidev resolves to the runtime's
// own export addon (addon/, brand.AddonPkg): the package name and the short
// name. Slidev tries @slidev/addon-<name> and slidev-addon-<name> before the
// name itself, so a talk can't put another package in its place without
// bundling one with the addon's name in its own, which shadowsAddon counts.
var bundledAddons = map[string]bool{brand.AddonPkg: true, brand.AddonName: true}

// isBundledAddon reports whether an addons: entry (a JSON value) is exactly
// the name of the runtime's export addon.
func isBundledAddon(v json.RawMessage) bool {
	var s string
	return json.Unmarshal(v, &s) == nil && bundledAddons[s]
}

// shadowsAddon reports whether a bundled package (its folded name) could be
// found instead of the runtime's export addon: any package whose name holds
// the addon's. Such a package counts as code whatever it holds, since an addon
// can bring more than code files (an index.html, for one).
func shadowsAddon(pkg string) bool {
	return strings.Contains(pkg, fold(brand.AddonName))
}

// shadowsRuntime reports whether a bundled package (its folded name) is one
// the runtime has, one Slidev tries for a bundled theme (themePackages), or one
// Slidev or Vite loads from the talk's folder (talkLoads). Such a package counts
// as code whatever it holds. Slidev looks for a theme from the talk's folder
// up, so the talk's copy comes before the runtime's, and it puts the theme's
// index.html, scripts and all, into the page. Vite's root is the talk's folder,
// so the talk's node_modules comes first for the bare imports Slidev's own
// modules make. And Node runs a package's entry file as JavaScript whatever its
// extension, or with none. Pack never bundles a package the runtime has, so a
// talk only has one if it was made by hand.
func shadowsRuntime(pkg string) bool { return runtimeShadows[pkg] }

// themePackages are the packages Slidev tries, in order, for a theme name that
// isn't a path (createResolver in @slidev/cli): @slidev/theme-<name> and
// slidev-theme-<name> for an unscoped name, then the name itself. "none" loads
// no theme.
func themePackages(name string) []string {
	if name == "none" {
		return nil
	}
	if strings.Contains(name, "/") || strings.HasPrefix(name, "@") {
		return []string{name}
	}
	return []string{"@slidev/theme-" + name, "slidev-theme-" + name, name}
}

// talkLoads are packages the runtime lacks that Slidev 53 and Vite 8 import
// from the talk's folder when a talk needs them: vite-plugin-pwa for the pwa
// option, playwright-chromium for slidev export, and Vite's CSS preprocessors
// for .scss, .sass, .less, .styl and .sss files. A real one holds .js files, so
// counting it whatever it holds asks no more than before.
var talkLoads = []string{"vite-plugin-pwa", "playwright-chromium", "sass-embedded", "sass", "sugarss", "less", "stylus"}

// runtimeShadows are the folded names shadowsRuntime counts.
var runtimeShadows = func() map[string]bool {
	m := map[string]bool{}
	for pkg := range slidevrt.Lock() {
		m[fold(pkg)] = true
	}
	for name := range bundledThemes {
		for _, pkg := range themePackages(name) {
			m[fold(pkg)] = true
		}
	}
	for _, pkg := range talkLoads {
		m[pkg] = true
	}
	return m
}()

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
// isn't one the runtime bundles counts, and so does any addon but the export
// addon the runtime bundles (bundledAddons): each other one comes from the
// talk, a folder or a bundled package.
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
			if !isBundledAddon(a) {
				out = append(out, "the addon "+string(bytes.TrimSpace(a))+" (addons can run code)")
			}
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
	// plainAddons and bareAddons + addonItem are the allowed forms of an addons
	// line, a list of one name: `addons: [name]`, or `addons:` and `  - name` on
	// the next line. Group 2 is the name.
	plainAddons = regexp.MustCompile(`^addons:[ \t]*\[[ \t]*(["']?)([A-Za-z0-9@/._-]+)(["']?)[ \t]*\](?:[ \t]+#.*)?[ \t]*$`)
	bareAddons  = regexp.MustCompile(`^addons:(?:[ \t]+#.*)?[ \t]*$`)
	addonItem   = regexp.MustCompile(`^[ \t]+-[ \t]+(["']?)([A-Za-z0-9@/._-]+)(["']?)(?:[ \t]+#.*)?[ \t]*$`)
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
//   - Addons count, but for the export addon the runtime bundles (bundledAddons),
//     set the same way, in one of two forms: `addons: [stagepack]` on one line,
//     or `addons:` and `  - stagepack` on the next. Every other addon comes from
//     the talk (a folder, or a bundled package).
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
		plain := !odd && i > 0 && i < headEnd && !flow // where an allowed form may be
		switch {
		case escapedKey.MatchString(l) || explicitKey.MatchString(l) || (i < headEnd && anchorOrAlias.MatchString(l)):
			out = append(out, "a YAML key in "+name+" (written in a way that can't be checked)")
		case themeKey.MatchString(l):
			m := plainTheme.FindStringSubmatch(l)
			if plain && m != nil && m[1] == m[3] && bundledThemes[m[2]] && newKeyFollows(lines[i+1:]) {
				break // theme: default
			}
			if m := plainAddons.FindStringSubmatch(l); plain && m != nil && m[1] == m[3] && bundledAddons[m[2]] && newKeyFollows(lines[i+1:]) {
				continue // addons: [stagepack]; its [ is closed on the line, so flow stays as it is
			}
			if bareAddons.MatchString(l) && plain && i+1 < headEnd {
				if m := addonItem.FindStringSubmatch(lines[i+1]); m != nil && m[1] == m[3] && bundledAddons[m[2]] && newKeyFollows(lines[i+2:]) {
					break // addons:\n  - stagepack
				}
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
