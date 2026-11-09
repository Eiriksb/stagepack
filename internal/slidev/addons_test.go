package slidev

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"stagepack/internal/brand"
	slidevrt "stagepack/runtime"
)

func TestArgsNodeArgsFirst(t *testing.T) {
	c := Config{CLI: "/rt/slidev.mjs", Entry: "slides.md", NodeArgs: []string{"--import", "file:///x.mjs"}}
	want := []string{"--import", "file:///x.mjs", "/rt/slidev.mjs", "./slides.md", "--port", "4000", "--open", "false", "--log", "warn"}
	if got := Args(c, 4000); !reflect.DeepEqual(got, want) {
		t.Fatalf("Args = %q, want %q", got, want)
	}
}

func TestChildEnvAddsEnv(t *testing.T) {
	env := childEnv([]string{"B=2", "BROWSER=firefox", "NODE_OPTIONS=--max-old-space-size=4096"}, "A=1")
	at := slices.Index(env, "A=1")
	if at < 0 {
		t.Fatalf("A=1 is missing: %q", env)
	}
	for _, kv := range []string{"B=2", "BROWSER=none", brand.ViewerEnv + "=1", "NODE_OPTIONS=--max-old-space-size=4096 --dns-result-order=ipv4first"} {
		if i := slices.Index(env, kv); i < 0 || i > at {
			t.Fatalf("%s is at %d, want it before A=1 (at %d): %q", kv, i, at, env)
		}
	}
	if slices.Contains(env, "BROWSER=firefox") {
		t.Fatalf("the rewrites were skipped: %q", env)
	}
}

// Start gives Slidev the Config's NodeArgs before the CLI and its Env.
func TestStartPassesNodeArgsAndEnv(t *testing.T) {
	c := fakeConfig(t, "echo")
	c.NodeArgs = []string{"--import", "file:///x.mjs"}
	c.Env = []string{"STAGEPACK_TEST_EXTRA=1"}
	port, _ := FreePort()
	if s, err := Start(context.Background(), c, port); err == nil {
		s.Stop()
		t.Fatal("Start succeeded, but the echo fake exits")
	}
	data, err := os.ReadFile(c.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Args, Env []string }
	for line := range strings.Lines(string(data)) {
		if strings.HasPrefix(line, "{") {
			if err := json.Unmarshal([]byte(line), &got); err != nil {
				t.Fatal(err)
			}
		}
	}
	if want := Args(c, port); !reflect.DeepEqual(got.Args, want) {
		t.Fatalf("args = %q, want %q\nlog:\n%s", got.Args, want, data)
	}
	if !slices.Contains(got.Env, "STAGEPACK_TEST_EXTRA=1") {
		t.Fatalf("Env did not reach the child: %q", got.Env)
	}
}

// preloadFile is the path a file URL from AddonLaunch names.
func preloadFile(t *testing.T, s string) string {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "file" || u.Host != "" || !strings.HasPrefix(s, "file:///") {
		t.Fatalf("%q is not a local file URL", s)
	}
	if runtime.GOOS == "windows" {
		return filepath.FromSlash(strings.TrimPrefix(u.Path, "/"))
	}
	return u.Path
}

func TestAddonLaunchPaths(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home a%b#c")
	nodeArgs, env, err := AddonLaunch(home, []string{"/a b/x"})
	if err != nil {
		t.Fatal(err)
	}
	wantEnv := ExtraAddonsEnv + `=["/a b/x"]`
	if runtime.GOOS == "windows" {
		abs, _ := filepath.Abs("/a b/x")
		list, _ := json.Marshal([]string{filepath.ToSlash(abs)})
		wantEnv = ExtraAddonsEnv + "=" + string(list)
	}
	if !reflect.DeepEqual(env, []string{wantEnv}) {
		t.Fatalf("env = %q, want %q", env, []string{wantEnv})
	}
	if len(nodeArgs) != 2 || nodeArgs[0] != "--import" {
		t.Fatalf("nodeArgs = %q, want --import and a file URL", nodeArgs)
	}
	if strings.ContainsAny(nodeArgs[1], " #") {
		t.Fatalf("the URL is not escaped: %q", nodeArgs[1])
	}
	p := preloadFile(t, nodeArgs[1])
	sum := sha256.Sum256(addonsPreload)
	if want := filepath.Join(home, "lib", "addons-preload-"+hex.EncodeToString(sum[:])[:12]+".mjs"); p != want {
		t.Fatalf("the preload is at %s, want %s", p, want)
	}
	if data, err := os.ReadFile(p); err != nil || !bytes.Equal(data, addonsPreload) {
		t.Fatalf("the preload file does not hold the embedded preload (err %v)", err)
	}

	// A second launch finds the file and leaves it alone.
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	again, _, err := AddonLaunch(home, []string{"/a b/x"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, nodeArgs) {
		t.Fatalf("second nodeArgs = %q, want %q", again, nodeArgs)
	}
	if fi, err := os.Stat(p); err != nil || !fi.ModTime().Equal(old) {
		t.Fatalf("the second launch rewrote the preload (err %v)", err)
	}
	if left, _ := os.ReadDir(filepath.Join(home, "lib")); len(left) != 1 {
		t.Fatalf("lib holds %d entries, want only the preload", len(left))
	}

	// A relative addon is made absolute.
	_, env, err = AddonLaunch(home, []string{filepath.Join("rel", "x")})
	if err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	list, _ := json.Marshal([]string{filepath.ToSlash(filepath.Join(cwd, "rel", "x"))})
	if want := ExtraAddonsEnv + "=" + string(list); !reflect.DeepEqual(env, []string{want}) {
		t.Fatalf("env = %q, want %q", env, want)
	}
}

func TestAddonLaunchNone(t *testing.T) {
	home := t.TempDir()
	nodeArgs, env, err := AddonLaunch(home, nil)
	if nodeArgs != nil || env != nil || err != nil {
		t.Fatalf("AddonLaunch(no addons) = %q, %q, %v; want nil, nil, nil", nodeArgs, env, err)
	}
	if _, err := os.Stat(filepath.Join(home, "lib")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("no addons, but lib was made (err %v)", err)
	}
}

func TestAddonLaunchFileURL(t *testing.T) {
	for in, want := range map[string]string{
		"/home/u/a b/x.mjs":    "file:///home/u/a%20b/x.mjs",
		"/h/100%#?.mjs":        "file:///h/100%25%23%3F.mjs",
		"C:/Users/u/a b/x.mjs": "file:///C:/Users/u/a%20b/x.mjs",
	} {
		if got := fileURL(in); got != want {
			t.Errorf("fileURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// linuxRuntime returns dist/linux's node and Slidev CLI, or skips when they are missing,
// as test/e2e_test.go linuxBuild does.
func linuxRuntime(t *testing.T) (node, cli string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the runtime tests run the Linux build in dist/linux")
	}
	home, err := filepath.Abs(filepath.Join("..", "..", "dist", "linux"))
	if err != nil {
		t.Fatal(err)
	}
	node = filepath.Join(home, "node", "bin", "node")
	cli = filepath.Join(home, "runtimes", strconv.Itoa(slidevrt.Major()), "node_modules", "@slidev", "cli", "bin", "slidev.mjs")
	for _, p := range []string{node, cli} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("%s is missing (build it with: make runtime node OS=linux): skipping", p)
		}
	}
	return node, cli
}

// A fake Slidev runtime: dist/resolver-*.mjs with Slidev's caching getRoots, and a CLI that
// calls it the way Slidev does and prints what it got.
const (
	fakeResolver = `import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
let rootsInfo = null
async function getRoots(entry) {
	if (rootsInfo) return rootsInfo
	if (!entry) throw new Error('no entry')
	rootsInfo = { entry, userPkgJson: JSON.parse(readFileSync(join(dirname(entry), 'package.json'), 'utf8')) }
	return rootsInfo
}
export { getRoots as n }
`
	fakeResolverWithout = "function other() {}\nexport { other as n }\n"
	fakeCLI             = `const { n } = await import('../dist/resolver-T3st.mjs')
console.log(JSON.stringify(await n('from-cli')))
`
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestAddonPreloadSeedsRoots runs the preload with node from dist/linux in front of a fake
// Slidev runtime.
func TestAddonPreloadSeedsRoots(t *testing.T) {
	node, _ := linuxRuntime(t)
	rt := t.TempDir()
	writeFiles(t, rt, map[string]string{
		"good/bin/slidev.mjs":            fakeCLI,
		"good/dist/resolver-T3st.mjs":    fakeResolver,
		"good/dist/serve-T3st.mjs":       "throw new Error('the preload imported more than resolver-*.mjs')\n",
		"without/bin/slidev.mjs":         fakeCLI,
		"without/dist/resolver-T3st.mjs": fakeResolverWithout,
	})
	nodeArgs, extra, err := AddonLaunch(t.TempDir(), []string{"/ed/a", "/ed/b"})
	if err != nil {
		t.Fatal(err)
	}
	var base []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, ExtraAddonsEnv+"=") {
			base = append(base, kv)
		}
	}
	// run starts the fake CLI with the preload in a talk folder holding pkg.
	run := func(t *testing.T, cli, pkg string, env ...string) (out map[string]any, stderr string, disk []byte, err error) {
		t.Helper()
		work := t.TempDir()
		writeFiles(t, work, map[string]string{"slides.md": "# Hi\n", "package.json": pkg})
		cmd := exec.Command(node, append(slices.Clone(nodeArgs), filepath.Join(rt, cli, "bin", "slidev.mjs"), "./slides.md", "--port", "1")...)
		cmd.Dir = work
		cmd.Env = childEnv(base, env...)
		var o, e bytes.Buffer
		cmd.Stdout, cmd.Stderr = &o, &e
		err = cmd.Run()
		if err == nil {
			if jerr := json.Unmarshal(o.Bytes(), &out); jerr != nil {
				t.Fatalf("output %q: %v (stderr %s)", o.Bytes(), jerr, e.Bytes())
			}
		}
		disk, rerr := os.ReadFile(filepath.Join(work, "package.json"))
		if rerr != nil {
			t.Fatal(rerr)
		}
		if out != nil {
			out["work"] = filepath.ToSlash(work)
		}
		return out, e.String(), disk, err
	}
	asJSON := func(t *testing.T, s string) any {
		t.Helper()
		var v any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	for _, tc := range []struct {
		name, pkg, wantPkg string
		env                []string
		seeded             bool // the preload called getRoots before the CLI
	}{
		{name: "appends to the talk's addons", env: extra, seeded: true,
			pkg:     `{"name":"talk","slidev":{"addons":["own"],"x":1}}`,
			wantPkg: `{"name":"talk","slidev":{"addons":["own","/ed/a","/ed/b"],"x":1}}`},
		{name: "adds a slidev field", env: extra, seeded: true,
			pkg:     `{"name":"talk"}`,
			wantPkg: `{"name":"talk","slidev":{"addons":["/ed/a","/ed/b"]}}`},
		{name: "does nothing when the variable is unset",
			pkg: `{"name":"talk"}`, wantPkg: `{"name":"talk"}`},
		{name: "does nothing for an empty list", env: []string{ExtraAddonsEnv + "=[]"},
			pkg: `{"name":"talk"}`, wantPkg: `{"name":"talk"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, disk, err := run(t, "good", tc.pkg, tc.env...)
			if err != nil {
				t.Fatalf("%v\n%s", err, stderr)
			}
			if string(disk) != tc.pkg {
				t.Fatalf("package.json on disk changed: %s", disk)
			}
			if !reflect.DeepEqual(out["userPkgJson"], asJSON(t, tc.wantPkg)) {
				t.Fatalf("userPkgJson = %v, want %s", out["userPkgJson"], tc.wantPkg)
			}
			wantEntry := "from-cli"
			if tc.seeded {
				wantEntry = out["work"].(string) + "/slides.md"
			}
			if out["entry"] != wantEntry {
				t.Fatalf("getRoots got entry %v first, want %s", out["entry"], wantEntry)
			}
		})
	}

	t.Run("fails without getRoots", func(t *testing.T) {
		_, stderr, _, err := run(t, "without", `{"name":"talk"}`, extra...)
		if err == nil {
			t.Fatal("the preload let Slidev start without getRoots")
		}
		if want := "stagepack: this Slidev runtime has no getRoots; extra addons can't be loaded"; !strings.Contains(stderr, want) {
			t.Fatalf("stderr does not say %q:\n%s", want, stderr)
		}
	})
}

// treeSums maps every folder and file below dir to "dir" or the file's sha256.
func treeSums(t *testing.T, dir string) map[string]string {
	t.Helper()
	sums := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			sums[rel] = "dir"
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		sums[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sums
}

func sumsDiff(before, after map[string]string) []string {
	var diff []string
	for p, s := range before {
		if a, ok := after[p]; !ok {
			diff = append(diff, "removed "+p)
		} else if a != s {
			diff = append(diff, "changed "+p)
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			diff = append(diff, "added "+p)
		}
	}
	sort.Strings(diff)
	return diff
}

// TestAddonPreloadLoadsProbe runs the real Slidev from dist/linux on a copy of testdata/deck,
// with and without AddonLaunch for testdata/addons/probe. Only the launch loads the probe, and
// neither run changes a byte of the talk.
func TestAddonPreloadLoadsProbe(t *testing.T) {
	node, cli := linuxRuntime(t)
	deck := filepath.Join(t.TempDir(), "deck")
	if err := os.CopyFS(deck, os.DirFS(filepath.Join("..", "..", "testdata", "deck"))); err != nil {
		t.Fatal(err)
	}
	before := treeSums(t, deck)
	probe, err := filepath.Abs(filepath.Join("..", "..", "testdata", "addons", "probe"))
	if err != nil {
		t.Fatal(err)
	}
	nodeArgs, env, err := AddonLaunch(t.TempDir(), []string{probe})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}

	// serve starts Slidev on the deck, asks for the probe's path and stops Slidev.
	serve := func(t *testing.T, nodeArgs, env []string) (code int, body, log string) {
		t.Helper()
		c := Config{Node: node, CLI: cli, WorkDir: deck, Entry: "slides.md",
			LogPath: filepath.Join(t.TempDir(), "slidev.log"), NodeArgs: nodeArgs, Env: env}
		port, err := FreePort()
		if err != nil {
			t.Fatal(err)
		}
		s, err := Start(context.Background(), c, port)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Stop()
		resp, err := client.Get(s.BaseURL() + "/__probe-addon")
		if err != nil {
			t.Fatalf("%v\n%s", err, LogTail(c.LogPath))
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Stop(); err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(data), LogTail(c.LogPath)
	}
	unchanged := func(t *testing.T) {
		t.Helper()
		if diff := sumsDiff(before, treeSums(t, deck)); len(diff) > 0 {
			t.Fatalf("Slidev changed the talk's folder: %q", diff)
		}
	}

	t.Run("without AddonLaunch", func(t *testing.T) {
		code, body, log := serve(t, nil, nil)
		if code == http.StatusOK && body == "ok" {
			t.Fatalf("the probe answered without AddonLaunch\n%s", log)
		}
		t.Logf("/__probe-addon without the launch: %d, %d bytes", code, len(body))
		unchanged(t)
	})
	t.Run("with AddonLaunch", func(t *testing.T) {
		code, body, log := serve(t, nodeArgs, env)
		if code != http.StatusOK || body != "ok" {
			t.Fatalf("/__probe-addon = %d %q, want 200 \"ok\"\n%s", code, body, log)
		}
		unchanged(t)
	})
}

// A preload a crash left short (or anything else at its name) is replaced, so the addons are
// never silently missing.
func TestAddonLaunchReplacesABrokenPreload(t *testing.T) {
	for name, content := range map[string][]byte{
		"empty":     {},
		"truncated": addonsPreload[:len(addonsPreload)/2],
		"garbage":   []byte("not a preload\n"),
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			sum := sha256.Sum256(addonsPreload)
			p := filepath.Join(home, "lib", "addons-preload-"+hex.EncodeToString(sum[:])[:12]+".mjs")
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, content, 0o644); err != nil {
				t.Fatal(err)
			}
			nodeArgs, _, err := AddonLaunch(home, []string{"/a"})
			if err != nil {
				t.Fatal(err)
			}
			if got := preloadFile(t, nodeArgs[1]); got != p {
				t.Fatalf("the preload is at %s, want %s", got, p)
			}
			if data, err := os.ReadFile(p); err != nil || !bytes.Equal(data, addonsPreload) {
				t.Fatalf("the broken preload was kept (%d bytes, err %v)", len(data), err)
			}
			if left, _ := os.ReadDir(filepath.Dir(p)); len(left) != 1 {
				t.Fatalf("lib holds %d entries, want only the preload", len(left))
			}
		})
	}
}
