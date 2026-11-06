package runtime

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"stagepack/internal/brand"
)

func TestLockPinsSlidev(t *testing.T) {
	if got := SlidevVersion(); got != "53.0.0" {
		t.Errorf("SlidevVersion() = %q, want 53.0.0", got)
	}
	if got := Major(); got != 53 {
		t.Errorf("Major() = %d, want 53", got)
	}
	lock := Lock()
	if got := lock["@fontsource-variable/inter"]; got != "5.3.0" {
		t.Errorf(`Lock()["@fontsource-variable/inter"] = %q, want 5.3.0`, got)
	}
	if got := lock["@slidev/cli"]; got != "53.0.0" {
		t.Errorf(`Lock()["@slidev/cli"] = %q, want 53.0.0`, got)
	}
	// only top-level packages: nested node_modules/a/node_modules/b entries are not in it
	for name := range lock {
		if name == "" || filepath.Base(filepath.Dir(name)) == "node_modules" {
			t.Errorf("Lock() has a nested or empty name %q", name)
		}
	}
}

// build/runtime.sh installs into runtimes/<major>/ with major spelled out; it must agree with Major().
func TestRuntimeScriptMajor(t *testing.T) {
	sh, err := os.ReadFile("../build/runtime.sh")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^major=(\d+)`).FindSubmatch(sh)
	if m == nil {
		t.Fatal("no major= line in build/runtime.sh")
	}
	if got, _ := strconv.Atoi(string(m[1])); got != Major() {
		t.Errorf("build/runtime.sh major=%d, runtime.Major() = %d", got, Major())
	}
}

// build/payload.sh zips runtimes/<major>/ with major spelled out; it must agree with Major().
func TestPayloadScriptMajor(t *testing.T) {
	sh, err := os.ReadFile("../build/payload.sh")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^major=(\d+)`).FindSubmatch(sh)
	if m == nil {
		t.Fatal("no major= line in build/payload.sh")
	}
	if got, _ := strconv.Atoi(string(m[1])); got != Major() {
		t.Errorf("build/payload.sh major=%d, runtime.Major() = %d", got, Major())
	}
}

// After `make runtime OS=win32`, every win32-x64 native package in the lock is in the Windows tree.
func TestWindowsRuntimeHasWin32Natives(t *testing.T) {
	nm := filepath.Join("..", "dist", "windows", "runtimes", strconv.Itoa(Major()), "node_modules")
	if _, err := os.Stat(nm); err != nil {
		t.Skip("no Windows runtime built (make runtime OS=win32)")
	}
	var lock struct {
		Packages map[string]struct {
			OS  []string `json:"os"`
			CPU []string `json:"cpu"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(lockJSON, &lock); err != nil {
		t.Fatal(err)
	}
	n := 0
	for key, p := range lock.Packages {
		if !slices.Contains(p.OS, "win32") || !slices.Contains(p.CPU, "x64") {
			continue
		}
		n++
		dir := filepath.Join("..", "dist", "windows", "runtimes", strconv.Itoa(Major()), filepath.FromSlash(key))
		if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
			t.Errorf("%s: not in the Windows runtime (%v)", key, err)
		}
	}
	if n == 0 {
		t.Error("the lock lists no win32-x64 packages; expected the rolldown/oxc/lightningcss bindings")
	}
}

// build/addon.sh, build/release.sh and build/runtime-stamp.sh name the runtime's folder and the export addon too.
func TestAddonAndReleaseScriptsAgree(t *testing.T) {
	for _, f := range []string{"../build/addon.sh", "../build/release.sh", "../build/runtime-stamp.sh"} {
		sh, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		m := regexp.MustCompile(`(?m)^major=(\d+)`).FindSubmatch(sh)
		if m == nil {
			t.Fatalf("no major= line in %s", f)
		}
		if got, _ := strconv.Atoi(string(m[1])); got != Major() {
			t.Errorf("%s major=%d, runtime.Major() = %d", f, got, Major())
		}
	}
	sh, err := os.ReadFile("../build/addon.sh")
	if err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`(?m)^pkg=(\S+)`).FindSubmatch(sh); m == nil || string(m[1]) != brand.AddonPkg {
		t.Errorf("build/addon.sh pkg= is not %s", brand.AddonPkg)
	}
	var pj struct{ Name, License string }
	data, err := os.ReadFile("../addon/package.json")
	if err == nil {
		err = json.Unmarshal(data, &pj)
	}
	if err != nil {
		t.Fatal(err)
	}
	if pj.Name != brand.AddonPkg || pj.License != "MIT" {
		t.Errorf("addon/package.json: name %q, license %q; want %s, MIT", pj.Name, pj.License, brand.AddonPkg)
	}
}

// scriptRoot makes a stand-in for the repository in a temporary folder: build/runtime.sh, build/release.sh and
// build/runtime-stamp.sh as they are, a runtime/ with a vendored tarball, and stubs for the steps that would run
// npm, node or the addon copy. It returns the root and a PATH whose first entry holds stub npm and node programs:
// npm ci fails unless vendor/slidev-pptx-export-0.4.0-dev.tgz is in its working directory.
func scriptRoot(t *testing.T) (root, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the build scripts are bash scripts for Linux")
	}
	for _, tool := range []string{"bash", "sha256sum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("no %s", tool)
		}
	}
	root = t.TempDir()
	write := func(rel, content string, mode os.FileMode) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"runtime.sh", "release.sh", "runtime-stamp.sh", "versions.env"} {
		data, err := os.ReadFile(filepath.Join("..", "build", f))
		if err != nil {
			t.Fatal(err)
		}
		write("build/"+f, string(data), 0o755)
	}
	write("build/addon.sh", "#!/usr/bin/env bash\nexit 0\n", 0o755)
	write("build/licenses.mjs", "", 0o644)
	write("runtime/package.json", "{}", 0o644)
	write("runtime/package-lock.json", "{}", 0o644)
	write("runtime/pnpm-workspace.yaml", "x: 1\n", 0o644)
	write("runtime/vendor/slidev-pptx-export-0.4.0-dev.tgz", "tarball", 0o644)
	stubs := t.TempDir()
	if err := os.WriteFile(filepath.Join(stubs, "npm"), []byte(`#!/usr/bin/env bash
[ "$1" = ci ] || exit 0
[ -f vendor/slidev-pptx-export-0.4.0-dev.tgz ] || { echo "npm error: no vendor/slidev-pptx-export-0.4.0-dev.tgz in $PWD" >&2; exit 1; }
mkdir -p node_modules
`), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stubs, "node"), []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root, stubs + string(os.PathListSeparator) + os.Getenv("PATH")
}

func runScript(t *testing.T, root, path string, args ...string) (out string, err error) {
	t.Helper()
	cmd := exec.Command("bash", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+path)
	b, err := cmd.CombinedOutput()
	return string(b), err
}

// build/runtime.sh copies runtime/vendor beside the lock before npm ci (the lock's file:vendor/... entry is
// relative to it) and does not leave the copy in what is shipped.
func TestRuntimeScriptInstallsTheVendoredTarball(t *testing.T) {
	root, path := scriptRoot(t)
	if out, err := runScript(t, root, path, "build/runtime.sh", "linux"); err != nil {
		t.Fatalf("build/runtime.sh linux: %v\n%s", err, out)
	}
	dir := filepath.Join(root, "dist", "linux", "runtimes", strconv.Itoa(Major()))
	if _, err := os.Stat(filepath.Join(dir, "vendor")); err == nil {
		t.Error("the vendor copy was left in the runtime")
	}
	if _, err := os.Stat(filepath.Join(dir, "node_modules")); err != nil {
		t.Errorf("no node_modules: %v", err)
	}
}

func stampOf(t *testing.T, root, path string) string {
	t.Helper()
	out, err := runScript(t, root, path, "build/runtime-stamp.sh")
	if err != nil || len(strings.TrimSpace(out)) != 64 {
		t.Fatalf("build/runtime-stamp.sh: %v\n%s", err, out)
	}
	return strings.TrimSpace(out)
}

// The stamp covers every input: package.json, the lock, pnpm-workspace.yaml and each file in vendor/.
func TestRuntimeStampChangesWithEachInput(t *testing.T) {
	root, path := scriptRoot(t)
	base := stampOf(t, root, path)
	if again := stampOf(t, root, path); again != base {
		t.Fatalf("the stamp is not stable: %s then %s", base, again)
	}
	seen := map[string]string{base: "the start"}
	for _, f := range []string{"package.json", "package-lock.json", "pnpm-workspace.yaml", "vendor/slidev-pptx-export-0.4.0-dev.tgz", "vendor/another.tgz"} {
		p := filepath.Join(root, "runtime", f)
		old, _ := os.ReadFile(p)
		if err := os.WriteFile(p, append(old, 'x'), 0o644); err != nil {
			t.Fatal(err)
		}
		got := stampOf(t, root, path)
		if was, dup := seen[got]; dup {
			t.Errorf("changing %s gives the stamp of %s", f, was)
		}
		seen[got] = f
	}
}

// build/runtime.sh writes the stamp that `build/runtime-stamp.sh check` and `build/release.sh pack` compare.
func TestRuntimeScriptWritesTheStampAndPackChecksIt(t *testing.T) {
	root, path := scriptRoot(t)
	if out, err := runScript(t, root, path, "build/runtime.sh", "linux"); err != nil {
		t.Fatalf("build/runtime.sh linux: %v\n%s", err, out)
	}
	if out, err := runScript(t, root, path, "build/runtime.sh", "win32"); err != nil {
		t.Fatalf("build/runtime.sh win32: %v\n%s", err, out)
	}
	stamp := filepath.Join(root, "dist", "linux", "runtimes", strconv.Itoa(Major()), ".runtime-stamp")
	if got, err := os.ReadFile(stamp); err != nil || strings.TrimSpace(string(got)) != stampOf(t, root, path) {
		t.Fatalf("the stamp file holds %q (%v), want %s", got, err, stampOf(t, root, path))
	}
	const different = "was built from a different runtime/"
	// a matching stamp passes the check (pack goes on and stops at what the stub dist lacks)
	out, err := runScript(t, root, path, "build/release.sh", "pack", "0.0.1")
	if err == nil || strings.Contains(out, different) || !strings.Contains(out, "missing") {
		t.Errorf("matching stamps: pack = %v\n%s", err, out)
	}
	// a changed input makes both dist runtimes stale
	if err := os.WriteFile(filepath.Join(root, "runtime", "vendor", "slidev-pptx-export-0.4.0-dev.tgz"), []byte("newer"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = runScript(t, root, path, "build/release.sh", "pack", "0.0.1")
	want := "dist/linux/runtimes/53 " + different + ": run make runtime OS=linux"
	if err == nil || !strings.Contains(out, want) {
		t.Errorf("a changed vendor file: pack = %v\n%s\nwant %q", err, out, want)
	}
	// the Windows runtime alone out of date
	if err := os.WriteFile(filepath.Join(root, "runtime", "vendor", "slidev-pptx-export-0.4.0-dev.tgz"), []byte("tarball"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dist", "windows", "runtimes", "53", ".runtime-stamp"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = runScript(t, root, path, "build/release.sh", "pack", "0.0.1")
	want = "dist/windows/runtimes/53 " + different + ": run make runtime OS=win32"
	if err == nil || !strings.Contains(out, want) {
		t.Errorf("a stale windows stamp: pack = %v\n%s\nwant %q", err, out, want)
	}
}
