package deck

import (
	"encoding/json"
	"strings"
	"testing"

	"stagepack/internal/brand"
)

func TestCodeReasons(t *testing.T) {
	plain := "---\ntheme: default\n---\n# hi\n"
	cases := []struct {
		name   string
		names  []string
		slides string
		want   []string
		files  map[string]string // other entries' content (package.json files)
	}{
		{"plain talk", []string{brand.ManifestName, "slides.md", "style.css", "public/", "public/clip.mp4", "images/a.png"}, plain, nil, nil},
		{"scripts in public count too", []string{"slides.md", "public/player.js", "public/x.vue", "public/a.css"}, plain, []string{"public/player.js", "public/x.vue"}, nil},
		{"css-only package", []string{"slides.md", "vendor/node_modules/@fontsource/inter/index.css", "vendor/node_modules/@fontsource/inter/package.json", "vendor/node_modules/@fontsource/inter/files/a.woff2"}, plain, nil, nil},
		{"vite config", []string{"slides.md", "vite.config.ts"}, plain, []string{"vite.config.ts (configuration that runs as code)"}, nil},
		{"config any case and extension", []string{"Vite.Config.MJS", "uno.config.js", "unocss.config.ts", "postcss.config.cjs", ".postcssrc.json"}, plain, []string{
			"Vite.Config.MJS (configuration that runs as code)", "uno.config.js (configuration that runs as code)",
			"unocss.config.ts (configuration that runs as code)", "postcss.config.cjs (configuration that runs as code)",
			".postcssrc.json (configuration that runs as code)"}, nil},
		{"config names count anywhere, any extension or none", []string{"docs/vite.config.md", "uno.config"}, plain, []string{
			"docs/vite.config.md (configuration that runs as code)", "uno.config (configuration that runs as code)"}, nil},
		{"similar names are not config", []string{"vite-config.md", "unoconfig.css"}, plain, nil, nil},
		{"anything in setup", []string{"setup/", "setup/main.ts", "setup/shiki.json"}, plain, []string{"setup/main.ts (setup code)", "setup/shiki.json (setup code)"}, nil},
		{"components and scripts", []string{"components/Counter.vue", "snippets/a.ts", "lib/x.mjs", "lib/y.cjs", "a.jsx", "b.tsx", "c.mts", "d.cts", "e.wasm", "f.node"}, plain, []string{
			"components/Counter.vue", "snippets/a.ts", "lib/x.mjs", "lib/y.cjs", "a.jsx", "b.tsx", "c.mts", "d.cts", "e.wasm", "f.node"}, nil},
		{"package with code, once", []string{"vendor/node_modules/foo/package.json", "vendor/node_modules/foo/index.js", "vendor/node_modules/foo/lib/b.js",
			"vendor/node_modules/@org/bar/dist/x.mjs", "vendor/node_modules/foo/node_modules/baz/index.js"}, plain, []string{
			"package foo (bundled with the talk)", "package @org/bar (bundled with the talk)"}, nil},
		{"node_modules straight in the zip", []string{"node_modules/evil/index.js"}, plain, []string{"package evil (bundled with the talk)"}, nil},
		{"package.json listing addons", []string{"slides.md", "package.json"}, plain,
			[]string{"package.json (it lists Slidev addons, which can run code)"},
			map[string]string{"package.json": `{"slidev": {"addons": ["slidev-addon-x"]}}`}},
		{"package.json keys are exact, as in JavaScript", []string{"slides.md", "package.json"}, plain,
			[]string{"package.json (it lists Slidev addons, which can run code)"},
			map[string]string{"package.json": `{"slidev": {"addons": ["x"]}, "SLIDEV": {"addons": []}}`}},
		{"package.json without addons", []string{"slides.md", "package.json"}, plain, nil,
			map[string]string{"package.json": `{"slidev": {"addons": []}, "devDependencies": {"@slidev/cli": "53"}}`}},
		{"PostCSS config in package.json", []string{"slides.md", "package.json"}, plain,
			[]string{`package.json ("postcss": configuration that runs as code)`},
			map[string]string{"package.json": `{"postcss": {"plugins": {"./x": {}}}}`}},
		{"bundled package listing addons", []string{"vendor/node_modules/a/package.json", "vendor/node_modules/a/index.css"}, plain,
			[]string{"package a (bundled with the talk)"},
			map[string]string{"vendor/node_modules/a/package.json": `{"slidev": {"addons": ["b"]}}`}},
		{"broken package.json", []string{"slides.md", "package.json"}, plain, []string{"package.json (it can't be read, so its addons can't be checked)"}, map[string]string{"package.json": `{`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			read := func(name string) []byte {
				if name == "slides.md" {
					return []byte(c.slides)
				}
				if d, ok := c.files[name]; ok {
					return []byte(d)
				}
				return nil
			}
			got := CodeReasons(c.names, read)
			if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Errorf("CodeReasons = %q, want %q", got, c.want)
			}
		})
	}
}

// A YAML parser may read a theme or addons out of lines that a line-by-line
// look misreads. Each of these must count as code (or be asked about).
func TestCodeReasonsFailClosedOnYAML(t *testing.T) {
	for name, slides := range map[string]string{
		"lone CR line breaks":        "---\rtheme: seriph\rtheme: ./x\r---\r# hi\n",
		"NEL line breaks":            "---\u0085theme: ./x\u0085---\u0085",
		"line separator":             "---\u2028theme: ./x\u2028---\n",
		"folded scalar":              "---\ntheme: >-\n  ./x\n---\n",
		"plain scalar on two lines":  "---\ntheme: seriph\n  ./x\n---\n",
		"alias":                      "---\nt: &a ./x\ntheme: *a\n---\n",
		"tag":                        "---\ntheme: !!str ./x\n---\n",
		"tagged key":                 "---\n!!str theme: ./x\n---\n",
		"flow mapping":               "---\n{theme: ./x}\n---\n",
		"flow mapping later key":     "---\n{title: a, theme: seriph, addons: [./x]}\n---\n",
		"JSON style":                 "---\n\"theme\":\"./x\"\n---\n",
		"escaped key":                "---\n\"th\\x65me\": ./x\n---\n",
		"explicit key":               "---\n? theme\n: ./x\n---\n",
		"escaped value":              "---\ntheme: \"\\x2e/x\"\n---\n",
		"yaml code block headmatter": "```yaml\ntheme: ./x\n```\n\n# hi\n",
		"later frontmatter":          "# hi\n\n---\ntheme: ./x\n---\n",
		"duplicate key":              "---\ntheme: seriph\ntheme: ./x\n---\n",
		"addons flow on two lines":   "---\naddons: [\n  ./x\n]\n---\n",
		"addons item goes on":        "---\naddons:\n  - seriph\n    ./x\n---\n",
		"addons nested list":         "---\naddons:\n  - - ./x\n---\n",
		"addons anchor":              "---\naddons: *list\n---\n",
		"quote inside a list":        "---\naddons: ['a,./x']\n---\n",
		"explicit key in a flow map": "---\n{? theme : ./x}\n---\n",
		"explicit key after a comma": "---\n{a: b, ? addons: [./x]}\n---\n",
		"flow value on the next line at column 0": "---\n{theme:\n./x}\n---\n",
		"flow value going on at column 0":         "---\n{theme: seriph\n./x}\n---\n",
		"theme as a list":                         "---\ntheme:\n- ./x\n---\n",
		"default going on":                        "---\ntheme: default\n  ./x\n---\n",
		"default going on after a blank line":     "---\ntheme: default\n\n  /x\n---\n",
		"default going on after a comment":        "---\ntheme: default\n# c\n  /x\n---\n",
		"default in a flow collection":            "---\nx: [\ntheme: default\n./x]\n---\n",
		"default in a yaml code block":            "```yaml\ntheme: default\n```\n",
		"default below the headmatter":            "---\ntitle: x\n---\n\ntheme: default\n",
		"default without headmatter":              "theme: default\n",
		"default unclosed headmatter":             "---\ntheme: default\n",
		"default with mismatched quotes":          "---\ntheme: \"default'\n---\n",
		"default as a later key":                  "---\ntheme: default\ntheme: ./x\n---\n",
		"prose":                                   "---\ntitle: x\n---\n\nThe theme: dark.\n",
		"empty theme":                             "---\ntheme:\n---\n",
		"comment without a blank":                 "---\ntheme: default#/../../evil\n---\n",
		"alias as the key":                        "---\nx: &k theme\n*k : ./x\n---\n",
		"merge key":                               "---\n<<: *base\ntheme: default\n---\n",
		"line separator after the plain form":     "---\ntheme: default\u2028theme: ./x\n---\n",
	} {
		if got := SlidesReasons("slides.md", slides, true); len(got) == 0 {
			t.Errorf("%s: not counted as code:\n%q", name, slides)
		}
	}
	// The allowed form counts only in the slides file.
	if got := SlidesReasons("pages/more.md", "---\ntheme: default\n---\n", false); len(got) != 1 {
		t.Errorf("theme in pages/more.md: %q", got)
	}
}

func TestSlidesReasonsNames(t *testing.T) {
	if got := SlidesReasons("slides.md", "---\ntheme: seriph\n---\n", true); len(got) != 1 || !strings.Contains(got[0], "a theme set in slides.md") {
		t.Errorf("seriph: %q", got)
	}
	if got := SlidesReasons("slides.md", "---\naddons:\n  - slidev-addon-qrcode\n---\n", true); len(got) != 1 || !strings.Contains(got[0], "addons set in slides.md") {
		t.Errorf("addons: %q", got)
	}
}

func TestHeadmatterReasons(t *testing.T) {
	for _, c := range []struct {
		theme, addons string
		want          int
	}{
		{``, ``, 0},
		{`null`, `null`, 0},
		{`"default"`, `[]`, 0},
		{`"none"`, `[]`, 0},
		{`""`, `[]`, 0},
		{`"@slidev/theme-default"`, `[]`, 0},
		{`"seriph"`, `[]`, 1},
		{`"./x"`, `[]`, 1},
		{`"default#/../../evil"`, `[]`, 1},
		{`{"default theme":"./x"}`, `[]`, 1},
		{`["default"]`, `[]`, 1},
		{`"default"`, `["slidev-addon-x"]`, 1},
		{`"default"`, `"./x"`, 1},
		{`"./x"`, `["./a"]`, 2},
		{`"default"`, `["a", "b", "./c"]`, 3},
	} {
		if got := HeadmatterReasons(json.RawMessage(c.theme), json.RawMessage(c.addons)); len(got) != c.want {
			t.Errorf("theme %s, addons %s: %q, want %d reasons", c.theme, c.addons, got, c.want)
		}
	}
}

// The reasons go into the version hash: two values must never give the same
// reasons, however long they are.
func TestHeadmatterReasonsKeepWholeValues(t *testing.T) {
	long := `"` + strings.Repeat("slidev-addon-padding-", 10)
	a := HeadmatterReasons(nil, json.RawMessage(`[`+long+`x"]`))
	b := HeadmatterReasons(nil, json.RawMessage(`[`+long+`y"]`))
	if strings.Join(a, "\n") == strings.Join(b, "\n") {
		t.Errorf("two addon lists that differ only at the end give the same reasons: %q", a)
	}
	a = HeadmatterReasons(json.RawMessage(long+`x"`), nil)
	b = HeadmatterReasons(json.RawMessage(long+`y"`), nil)
	if strings.Join(a, "\n") == strings.Join(b, "\n") {
		t.Errorf("two themes that differ only at the end give the same reasons: %q", a)
	}
}

func TestCodeReasonsPlainHeadmatter(t *testing.T) {
	for name, slides := range map[string]string{
		"no theme":             "---\ntitle: My talk\nfonts:\n  provider: none\n---\n# hi\n\nThe theme of this talk is plain.\n",
		"bundled theme":        "---\ntheme: default # the look\ntitle: My talk\n---\n# hi\n",
		"by package name":      "---\ntitle: x\ntheme: \"@slidev/theme-default\"\n---\n",
		"none":                 "---\ntheme: 'none'\n---\n",
		"CRLF":                 "---\r\ntheme: default\r\n---\r\n",
		"last line of block":   "---\ntitle: x\ntheme: default\n---\n# hi\n",
		"no headmatter at all": "# hi\n",
	} {
		if got := SlidesReasons("slides.md", slides, true); len(got) != 0 {
			t.Errorf("%s: counted as code: %q", name, got)
		}
	}
}

// Windows opens Package.json when Node asks for package.json: names are compared folded.
func TestCodeReasonsFoldsNames(t *testing.T) {
	addon := []byte(`{"slidev": {"addons": ["/elsewhere"]}}`)
	read := func(n string) []byte {
		if strings.HasSuffix(strings.ToLower(n), ".json") {
			return addon
		}
		return []byte("# hi\n")
	}
	for _, c := range []struct {
		names []string
		want  string
	}{
		{[]string{"slides.md", "Package.json"}, "Package.json (it lists Slidev addons, which can run code)"},
		{[]string{"slides.md", "Node_Modules/x/PACKAGE.JSON"}, "package x (bundled with the talk)"},
		{[]string{"slides.md", "themes/mine/package.json"}, "themes/mine/package.json (it lists Slidev addons, which can run code)"},
		{[]string{"SETUP/main.json"}, "SETUP/main.json (setup code)"},
		{[]string{"Vite.Config"}, "Vite.Config (configuration that runs as code)"},
		{[]string{"pac\u212Aage.json"}, "pac\u212Aage.json (it lists Slidev addons, which can run code)"}, // a Kelvin sign folds to k
		{[]string{"unocſs.config"}, "unocſs.config (configuration that runs as code)"},
		{[]string{"lib/x.MJS"}, "lib/x.MJS"},
	} {
		got := strings.Join(CodeReasons(c.names, read), "\n")
		if got != c.want {
			t.Errorf("%q: CodeReasons = %q, want %q", c.names, got, c.want)
		}
	}
}
