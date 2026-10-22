package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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

// build/addon.sh and build/release.sh name the runtime's folder and the export addon too.
func TestAddonAndReleaseScriptsAgree(t *testing.T) {
	for _, f := range []string{"../build/addon.sh", "../build/release.sh"} {
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
