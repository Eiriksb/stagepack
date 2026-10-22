//go:build e2e

package slidev_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"stagepack/internal/deck"
	"stagepack/internal/slidev"
)

// TestReadHeadmatterIntegration runs Slidev's own parser from dist/linux on
// headmatter written to fool a line-by-line check, and on plain headmatter.
func TestReadHeadmatterIntegration(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the integration test runs the Linux build in dist/linux")
	}
	home, err := filepath.Abs(filepath.Join("..", "..", "dist", "linux"))
	if err != nil {
		t.Fatal(err)
	}
	node := filepath.Join(home, "node", "bin", "node")
	rt := filepath.Join(home, "runtimes", "53")
	for _, p := range []string{node, filepath.Join(rt, "node_modules", "@slidev", "parser", "dist", "core.mjs")} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("%s is missing (build it with: make runtime node OS=linux): skipping", p)
		}
	}
	for _, c := range []struct {
		name, slides string
		code         bool
	}{
		{"plain", "---\ntheme: default\ntitle: x\n---\n# hi\n", false},
		{"no headmatter", "# hi\n\nThe theme: dark.\n", false},
		{"theme in a later slide", "# hi\n\n---\ntheme: ./x\n---\n", false},
		{"theme in a code block", "---\ntitle: x\n---\n\n```yaml\ntheme: ./x\n```\n", false},
		{"folder theme", "---\ntheme: ./x\n---\n", true},
		{"named theme", "---\ntheme: seriph\n---\n", true},
		{"alias as the key", "---\nx: &k theme\n*k : ./x\n---\n", true},
		{"comment without a blank", "---\ntheme: default#/../../evil\n---\n", true},
		{"line separator", "---\ntheme: default theme: ./x\n---\n", true},
		{"yaml code block headmatter", "```yaml\ntheme: ./x\n```\n# hi\n", true},
		{"flow mapping", "---\n{theme: ./x}\n---\n", true},
		{"addons", "---\naddons:\n  - slidev-addon-x\n---\n", true},
		{"addons under config", "---\nconfig:\n  addons: [./a]\n---\n", true},
		{"the export addon", "---\naddons:\n  - stagepack\n---\n", false},
		{"the export addon by package name", "---\naddons: [slidev-addon-stagepack]\n---\n", false},
		{"the export addon and another", "---\naddons: [stagepack, slidev-addon-x]\n---\n", true},
		{"the export addon as a folder", "---\naddons: [./stagepack]\n---\n", true},
	} {
		p := filepath.Join(t.TempDir(), "slides.md")
		if err := os.WriteFile(p, []byte(c.slides), 0o644); err != nil {
			t.Fatal(err)
		}
		h, err := slidev.ReadHeadmatter(context.Background(), node, rt, p)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		reasons := deck.HeadmatterReasons(h.Theme, h.Addons)
		if (len(reasons) > 0) != c.code {
			t.Errorf("%s: theme %s, addons %s: reasons %q, want code=%v", c.name, h.Theme, h.Addons, reasons, c.code)
		}
	}
}
