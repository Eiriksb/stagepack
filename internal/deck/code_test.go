package deck

import (
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
		{"theme by path", []string{"slides.md"}, "---\ntheme: ./my-theme\n---\n", []string{"theme ./my-theme in slides.md (loaded from a folder)"}, nil},
		{"theme by name", []string{"slides.md"}, "---\ntheme: seriph\n---\n", nil, nil},
		{"theme absolute and drive", []string{"slides.md"}, "---\ntheme: 'C:\\themes\\x'\n---\n", []string{`theme 'C:\themes\x' in slides.md (loaded from a folder)`}, nil},
		{"theme as Slidev reads paths", []string{"slides.md"}, "---\ntheme: themes/mine\naddons: ['@/addons/a', '@org/pkg', 'slidev-addon-x']\n---\n",
			[]string{"theme themes/mine in slides.md (loaded from a folder)", "addon @/addons/a in slides.md (loaded from a folder)"}, nil},
		{"addon by path in package.json", []string{"slides.md", "package.json"}, plain,
			[]string{"addon ./addon (loaded from a folder, named in package.json)"},
			map[string]string{"package.json": `{"slidev": {"addons": ["slidev-addon-x", "./addon", 3]}}`}},
		{"package.json without paths", []string{"slides.md", "package.json"}, plain, nil,
			map[string]string{"package.json": `{"slidev": {"addons": ["slidev-addon-x"]}, "devDependencies": {"@slidev/cli": "53"}}`}},
		{"bundled package naming an addon by path", []string{"vendor/node_modules/a/package.json", "vendor/node_modules/a/index.css"}, plain,
			[]string{"package a (bundled with the talk)"},
			map[string]string{"vendor/node_modules/a/package.json": `{"slidev": {"addons": ["../../../x"]}}`}},
		{"broken package.json", []string{"slides.md", "package.json"}, plain, []string{"package.json (it can't be read, so its addons can't be checked)"}, map[string]string{"package.json": `{`}},
		{"PostCSS config in package.json", []string{"slides.md", "package.json"}, plain,
			[]string{`package.json ("postcss": configuration that runs as code)`},
			map[string]string{"package.json": `{"postcss": {"plugins": {"./x": {}}}}`}},
		{"package.json keys are exact, as in JavaScript", []string{"slides.md", "package.json"}, plain,
			[]string{"addon ./x (loaded from a folder, named in package.json)"},
			map[string]string{"package.json": `{"slidev": {"addons": ["./x"]}, "SLIDEV": {"addons": []}}`}},
		{"addons inline", []string{"slides.md"}, "---\naddons: [slidev-addon-qrcode, '../addon', '@org/addon']\n---\n", []string{"addon ../addon in slides.md (loaded from a folder)"}, nil},
		{"addons block", []string{"slides.md"}, "---\naddons:\n  - slidev-addon-x\n  - /home/x/addon # local\ntitle: T\n---\n", []string{"addon /home/x/addon in slides.md (loaded from a folder)"}, nil},
		{"addons block without indent", []string{"slides.md"}, "---\r\naddons:\r\n- ~/addon\r\n---\r\n", []string{"addon ~/addon in slides.md (loaded from a folder)"}, nil},
		{"no headmatter", []string{"slides.md"}, "# theme: ./x\n", nil, nil},
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
			got := CodeReasons(c.names, "slides.md", read)
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
	} {
		read := func(string) []byte { return []byte(slides) }
		if got := CodeReasons([]string{"slides.md"}, "slides.md", read); len(got) == 0 {
			t.Errorf("%s: not counted as code:\n%q", name, slides)
		}
	}
	// Other Markdown files are looked at too.
	read := func(n string) []byte {
		if n == "pages/more.md" {
			return []byte("---\ntheme: ./x\n---\n")
		}
		return []byte("# hi\n")
	}
	if got := CodeReasons([]string{"slides.md", "pages/more.md"}, "slides.md", read); len(got) != 1 {
		t.Errorf("theme in pages/more.md: %q", got)
	}
}

func TestCodeReasonsPlainHeadmatter(t *testing.T) {
	for name, slides := range map[string]string{
		"named theme and addons": "---\ntheme: seriph # the look\ntitle: My talk\naddons:\n  - slidev-addon-qrcode\n  - '@org/addon'\nfonts:\n  provider: none\n---\n# hi\n",
		"quoted scoped theme":    "---\ntheme: \"@slidev/theme-default\"\naddons: [slidev-addon-x, \"@a/b\"]\n---\n",
		"empty theme":            "---\ntheme:\ntitle: x\n---\n",
		"prose":                  "---\ntitle: x\n---\n\nThe theme: dark, but readable.\n",
		"CRLF":                   "---\r\ntheme: default\r\n---\r\n",
	} {
		read := func(string) []byte { return []byte(slides) }
		if got := CodeReasons([]string{"slides.md"}, "slides.md", read); len(got) != 0 {
			t.Errorf("%s: counted as code: %q", name, got)
		}
	}
}
