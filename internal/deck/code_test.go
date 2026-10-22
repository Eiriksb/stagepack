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
		{"public scripts are served, not run", []string{"slides.md", "public/player.js", "public/x.vue"}, plain, nil, nil},
		{"css-only package", []string{"slides.md", "vendor/node_modules/@fontsource/inter/index.css", "vendor/node_modules/@fontsource/inter/package.json", "vendor/node_modules/@fontsource/inter/files/a.woff2"}, plain, nil, nil},
		{"vite config", []string{"slides.md", "vite.config.ts"}, plain, []string{"vite.config.ts (configuration that runs as code)"}, nil},
		{"config any case and extension", []string{"Vite.Config.MJS", "uno.config.js", "unocss.config.ts", "postcss.config.cjs", ".postcssrc.json"}, plain, []string{
			"Vite.Config.MJS (configuration that runs as code)", "uno.config.js (configuration that runs as code)",
			"unocss.config.ts (configuration that runs as code)", "postcss.config.cjs (configuration that runs as code)",
			".postcssrc.json (configuration that runs as code)"}, nil},
		{"config only counts at the top", []string{"docs/vite.config.md"}, plain, nil, nil},
		{"anything in setup", []string{"setup/", "setup/main.ts", "setup/shiki.json"}, plain, []string{"setup/main.ts (setup code)", "setup/shiki.json (setup code)"}, nil},
		{"components and scripts", []string{"components/Counter.vue", "snippets/a.ts", "lib/x.mjs", "lib/y.cjs", "a.jsx", "b.tsx", "c.mts", "d.cts", "e.wasm", "f.node"}, plain, []string{
			"components/Counter.vue", "snippets/a.ts", "lib/x.mjs", "lib/y.cjs", "a.jsx", "b.tsx", "c.mts", "d.cts", "e.wasm", "f.node"}, nil},
		{"package with code, once", []string{"vendor/node_modules/foo/package.json", "vendor/node_modules/foo/index.js", "vendor/node_modules/foo/lib/b.js",
			"vendor/node_modules/@org/bar/dist/x.mjs", "vendor/node_modules/foo/node_modules/baz/index.js"}, plain, []string{
			"package foo (bundled with the talk)", "package @org/bar (bundled with the talk)"}, nil},
		{"node_modules straight in the zip", []string{"node_modules/evil/index.js"}, plain, []string{"package evil (bundled with the talk)"}, nil},
		{"theme by path", []string{"slides.md"}, "---\ntheme: ./my-theme\n---\n", []string{"theme ./my-theme (loaded from a folder)"}, nil},
		{"theme by name", []string{"slides.md"}, "---\ntheme: seriph\n---\n", nil, nil},
		{"theme absolute and drive", []string{"slides.md"}, "---\ntheme: 'C:\\themes\\x'\n---\n", []string{`theme C:\themes\x (loaded from a folder)`}, nil},
		{"theme as Slidev reads paths", []string{"slides.md"}, "---\ntheme: themes/mine\naddons: ['@/addons/a', '@org/pkg', 'slidev-addon-x']\n---\n",
			[]string{"theme themes/mine (loaded from a folder)", "addon @/addons/a (loaded from a folder)"}, nil},
		{"addon by path in package.json", []string{"slides.md", "package.json"}, plain,
			[]string{"addon ./addon (loaded from a folder, named in package.json)"},
			map[string]string{"package.json": `{"slidev": {"addons": ["slidev-addon-x", "./addon", 3]}}`}},
		{"package.json without paths", []string{"slides.md", "package.json"}, plain, nil,
			map[string]string{"package.json": `{"slidev": {"addons": ["slidev-addon-x"]}, "devDependencies": {"@slidev/cli": "53"}}`}},
		{"bundled package naming an addon by path", []string{"vendor/node_modules/a/package.json", "vendor/node_modules/a/index.css"}, plain,
			[]string{"package a (bundled with the talk)"},
			map[string]string{"vendor/node_modules/a/package.json": `{"slidev": {"addons": ["../../../x"]}}`}},
		{"broken package.json", []string{"slides.md", "package.json"}, plain, nil, map[string]string{"package.json": `{`}},
		{"addons inline", []string{"slides.md"}, "---\naddons: [slidev-addon-qrcode, '../addon', '@org/addon']\n---\n", []string{"addon ../addon (loaded from a folder)"}, nil},
		{"addons block", []string{"slides.md"}, "---\naddons:\n  - slidev-addon-x\n  - /home/x/addon # local\ntitle: T\n---\n", []string{"addon /home/x/addon (loaded from a folder)"}, nil},
		{"addons block without indent", []string{"slides.md"}, "---\r\naddons:\r\n- ~/addon\r\n---\r\n", []string{"addon ~/addon (loaded from a folder)"}, nil},
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
