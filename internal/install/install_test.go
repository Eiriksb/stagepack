package install

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"stagepack/internal/brand"
	"stagepack/internal/trust"
)

const (
	testExe  = `C:\Users\Åse\AppData\Local\` + brand.Name + `\stagepack.exe`
	testIcon = testExe
)

func mustFind(t *testing.T, plan []RegValue, key, name string) string {
	t.Helper()
	for _, v := range plan {
		if v.Key == key && v.Name == name {
			return v.Value
		}
	}
	t.Fatalf("no value %q under %s in %+v", name, key, plan)
	return ""
}

func TestRegistryPlan(t *testing.T) {
	plan := RegistryPlan(testExe, testIcon)
	classes := `HKCU\Software\Classes\`
	uninst := `HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\` + brand.Name
	want := []RegValue{
		{classes + brand.Ext, "", brand.ProgID},
		{classes + brand.ProgID, "", brand.Name + " talk"},
		{classes + brand.ProgID + `\DefaultIcon`, "", `"` + testIcon + `"`},
		{classes + brand.ProgID + `\shell\open\command`, "", `"` + testExe + `" open "%1"`},
		{uninst, "DisplayName", brand.Name},
		{uninst, "UninstallString", `"` + testExe + `" uninstall`},
		{uninst, "InstallLocation", `C:\Users\Åse\AppData\Local\` + brand.Name},
		{uninst, "NoModify", "1"},
		{uninst, "NoRepair", "1"},
	}
	if !reflect.DeepEqual(plan, want) {
		t.Errorf("plan =\n%+v\nwant\n%+v", plan, want)
	}
	// Nothing outside the two allowed HKCU subtrees.
	for _, v := range plan {
		if !strings.HasPrefix(v.Key, classes) && !strings.HasPrefix(v.Key, uninst) {
			t.Errorf("key outside the allowed subtrees: %s", v.Key)
		}
	}
	for _, n := range []string{"NoModify", "NoRepair"} {
		if !isDWORD(n) {
			t.Errorf("%s should be a DWORD", n)
		}
	}
	if isDWORD("DisplayName") || isDWORD("") {
		t.Error("string values mistaken for DWORDs")
	}
}

func TestRegistryCommandQuotesPath(t *testing.T) {
	plan := RegistryPlan(testExe, testExe)
	got := mustFind(t, plan, `HKCU\Software\Classes\`+brand.ProgID+`\shell\open\command`, "")
	want := `"C:\Users\Åse\AppData\Local\` + brand.Name + `\stagepack.exe" open "%1"`
	if got != want {
		t.Errorf("command = %s, want %s", got, want)
	}
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(root, rel string) bool {
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

func payloadFS(version string) fstest.MapFS {
	return fstest.MapFS{
		"node/node.exe":            {Data: []byte("node " + version)},
		"runtimes/53/package.json": {Data: []byte(`{"v":"` + version + `"}`)},
	}
}

func TestInstallLayout(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := installFiles(home, payloadFS("1")); err != nil {
		t.Fatal(err)
	}
	if got := read(t, home, "node/node.exe"); got != "node 1" {
		t.Errorf("node.exe = %q", got)
	}
	if got := read(t, home, "runtimes/53/package.json"); got != `{"v":"1"}` {
		t.Errorf("package.json = %q", got)
	}

	// A reinstall replaces node/ and runtimes/ (no stale files) and keeps journal/ and work folders.
	write(t, home, "journal/x.json", "notes")
	write(t, home, "node/stale.txt", "old")
	write(t, home, "runtimes/52/old.txt", "old")
	write(t, home, "runtimes/53/work/abc/slides.md", "being shown")
	if err := installFiles(home, payloadFS("2")); err != nil {
		t.Fatal(err)
	}
	if got := read(t, home, "journal/x.json"); got != "notes" {
		t.Errorf("journal lost on reinstall: %q", got)
	}
	if got := read(t, home, "node/node.exe"); got != "node 2" {
		t.Errorf("node.exe = %q after reinstall", got)
	}
	if exists(home, "node/stale.txt") || exists(home, "runtimes/52") {
		t.Error("stale files survived the reinstall")
	}
	if got := read(t, home, "runtimes/53/work/abc/slides.md"); got != "being shown" {
		t.Errorf("work folder lost on reinstall: %q", got)
	}
	if exists(home, stagingName) {
		t.Error("staging folder left behind")
	}
	if left, _ := filepath.Glob(filepath.Join(home, ".install-*")); len(left) != 0 {
		t.Errorf("temporary folders left behind: %v", left)
	}
}

func TestInstallFilesFailureKeepsOldInstall(t *testing.T) {
	home := t.TempDir()
	if err := installFiles(home, payloadFS("1")); err != nil {
		t.Fatal(err)
	}
	bad := payloadFS("2")
	bad["runtimes/53/broken"] = &fstest.MapFile{Data: []byte("../../../outside"), Mode: fs.ModeSymlink}
	if err := installFiles(home, bad); err == nil {
		t.Fatal("a symlink leading out of the payload was accepted")
	}
	if got := read(t, home, "node/node.exe"); got != "node 1" {
		t.Errorf("old install damaged by a failed reinstall: %q", got)
	}
	if exists(home, stagingName) {
		t.Error("staging folder left behind after a failure")
	}
}

// installedHome makes home look as Install leaves it: the marker and the program.
func installedHome(t *testing.T, home string) {
	t.Helper()
	write(t, home, markerName(), "installed")
	write(t, home, ExeName(), "exe")
}

type fakeRegistry struct {
	written []RegValue
	deleted bool
	notices int
	err     error
}

func (f *fakeRegistry) register(exe string) error { f.written = RegistryPlan(exe, exe); return f.err }
func (f *fakeRegistry) remove() error             { f.deleted = true; return nil }
func (f *fakeRegistry) notify()                   { f.notices++ }

func TestInstallEndToEnd(t *testing.T) {
	home := filepath.Join(t.TempDir(), brand.Name)
	self := filepath.Join(t.TempDir(), "setup.exe")
	write(t, filepath.Dir(self), "setup.exe", "MZ-the-program")
	reg := &fakeRegistry{}
	if err := install(home, payloadFS("1"), self, reg); err != nil {
		t.Fatal(err)
	}
	if got := read(t, home, ExeName()); got != "MZ-the-program" {
		t.Errorf("installed program = %q", got)
	}
	if !exists(home, "node/node.exe") {
		t.Error("payload not unpacked")
	}
	exe := filepath.Join(home, ExeName())
	if want := RegistryPlan(exe, exe); !reflect.DeepEqual(reg.written, want) {
		t.Errorf("registry written = %+v, want %+v", reg.written, want)
	}
	if reg.notices != 1 {
		t.Errorf("shell notified %d times", reg.notices)
	}
	if !IsInstalled(home) {
		t.Error("the marker and the program should make IsInstalled true")
	}

	// Reinstalling from the installed copy itself must work (the file is its own source).
	if err := install(home, payloadFS("2"), exe, reg); err != nil {
		t.Fatal(err)
	}
	if got := read(t, home, ExeName()); got != "MZ-the-program" {
		t.Errorf("program after self-reinstall = %q", got)
	}
	if got := read(t, home, "node/node.exe"); got != "node 2" {
		t.Errorf("node.exe = %q", got)
	}
}

func TestInstallUsesProgramFromPayload(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	self := filepath.Join(t.TempDir(), "setup.exe")
	write(t, filepath.Dir(self), "setup.exe", "MZ-fat-with-payload")
	p := payloadFS("1")
	p[ExeName()] = &fstest.MapFile{Data: []byte("MZ-lean")}
	if err := install(home, p, self, &fakeRegistry{}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, home, ExeName()); got != "MZ-lean" {
		t.Errorf("installed program = %q, want the one from the payload", got)
	}
	// a reinstall replaces it, though the old one may be running
	p[ExeName()] = &fstest.MapFile{Data: []byte("MZ-lean-2")}
	if err := install(home, p, self, &fakeRegistry{}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, home, ExeName()); got != "MZ-lean-2" {
		t.Errorf("installed program after reinstall = %q", got)
	}
}

func TestInstallRegistryFailureIsReported(t *testing.T) {
	home := t.TempDir()
	self := filepath.Join(t.TempDir(), "s.exe")
	write(t, filepath.Dir(self), "s.exe", "x")
	boom := errors.New("access denied")
	if err := install(home, payloadFS("1"), self, &fakeRegistry{err: boom}); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the registry error", err)
	}
}

func TestUninstallKeepsJournal(t *testing.T) {
	home := t.TempDir()
	installedHome(t, home)
	write(t, home, "journal/x.json", "notes")
	write(t, home, "node/node.exe", "n")
	write(t, home, "runtimes/53/package.json", "{}")
	write(t, home, "logs/a.log", "l")
	write(t, home, "instances/a.json", "{}")
	write(t, home, trust.FileName, "{}")
	write(t, home, ".install-old-1/node/node.exe", "left over")
	reg := &fakeRegistry{}
	if _, err := uninstall(home, "", reg); err != nil {
		t.Fatal(err)
	}
	if got := read(t, home, "journal/x.json"); got != "notes" {
		t.Errorf("journal = %q", got)
	}
	for _, rel := range []string{"node", "runtimes", "logs", "instances", trust.FileName, ".install-old-1", ExeName(), markerName()} {
		if exists(home, rel) {
			t.Errorf("%s survived the uninstall", rel)
		}
	}
	ents, _ := os.ReadDir(home)
	if len(ents) != 1 || ents[0].Name() != "journal" {
		t.Errorf("home holds %v, want only journal", ents)
	}
	if !reg.deleted || reg.notices != 1 {
		t.Errorf("registry deleted=%v notices=%d", reg.deleted, reg.notices)
	}
	if !KeptJournal(home) {
		t.Error("KeptJournal = false")
	}
}

func TestUninstallRemovesEverythingWhenJournalEmpty(t *testing.T) {
	home := filepath.Join(t.TempDir(), "h")
	installedHome(t, home)
	write(t, home, "node/node.exe", "n")
	if err := os.MkdirAll(filepath.Join(home, "journal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := uninstall(home, "", &fakeRegistry{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("home should be gone, stat err = %v", err)
	}
	if KeptJournal(home) {
		t.Error("KeptJournal true for a missing journal")
	}
	// Uninstalling what is gone is refused, and harmless.
	if _, err := uninstall(home, "", &fakeRegistry{}); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("second uninstall: %v, want ErrNotInstalled", err)
	}
}

func TestUninstallLeavesRunningProgram(t *testing.T) {
	home := t.TempDir()
	installedHome(t, home)
	write(t, home, "node/node.exe", "n")
	self := filepath.Join(home, ExeName())
	left, err := uninstall(home, self, &fakeRegistry{})
	if err != nil {
		t.Fatal(err)
	}
	if !left {
		t.Error("expected the running program to be reported as left behind")
	}
	if !exists(home, ExeName()) || exists(home, "node") {
		t.Error("the running program should remain, the rest go")
	}
}

func TestUninstallRefusesAFolderThatIsNotAnInstall(t *testing.T) {
	// A profile folder, a drive root, a typo: nothing of ours is in it, so nothing is touched.
	for name, setup := range map[string]func(home string){
		"no marker, no program": func(h string) {},
		"program, no marker":    func(h string) { write(t, h, ExeName(), "exe") },
		"marker, no program":    func(h string) { write(t, h, markerName(), "x") },
		"node and runtimes only": func(h string) {
			write(t, h, "node/node.exe", "n")
			write(t, h, "runtimes/53/package.json", "{}")
		},
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			setup(home)
			write(t, home, "Documents/thesis.docx", "precious")
			write(t, home, "journal/x.json", "notes")
			write(t, home, "desktop.ini", "x")
			before := listing(t, home)
			reg := &fakeRegistry{}
			if _, err := uninstall(home, "", reg); !errors.Is(err, ErrNotInstalled) {
				t.Fatalf("err = %v, want ErrNotInstalled", err)
			}
			if after := listing(t, home); !reflect.DeepEqual(before, after) {
				t.Errorf("files changed: before %v after %v", before, after)
			}
			if reg.deleted || reg.notices != 0 {
				t.Error("the registry was touched for a folder that is not an install")
			}
		})
	}
}

// listing is every path below root.
func listing(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		out = append(out, p)
		return nil
	})
	return out
}

func TestUninstallKeepsFilesItDoesNotKnow(t *testing.T) {
	home := t.TempDir()
	installedHome(t, home)
	write(t, home, "node/node.exe", "n")
	write(t, home, "my-notes.txt", "mine")
	write(t, home, "Pictures/a.png", "p")
	if _, err := uninstall(home, "", &fakeRegistry{}); err != nil {
		t.Fatal(err)
	}
	if read(t, home, "my-notes.txt") != "mine" || read(t, home, "Pictures/a.png") != "p" {
		t.Error("an unknown file was deleted")
	}
	if exists(home, "node") || exists(home, ExeName()) || exists(home, markerName()) {
		t.Error("our own files should be gone")
	}
}

func TestUninstallRefusesWhileATalkIsOpen(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can move a read-only folder")
	}
	home := t.TempDir()
	installedHome(t, home)
	write(t, home, "node/node.exe", "n")
	write(t, home, "runtimes/53/package.json", "{}")
	// A folder that cannot be moved stands in for node.exe being in use.
	if err := os.Chmod(filepath.Join(home, "node"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(home, "node"), 0o755) })
	reg := &fakeRegistry{}
	_, err := uninstall(home, "", reg)
	if err == nil || !strings.Contains(err.Error(), "still open") {
		t.Fatalf("err = %v, want a refusal that says a talk is still open", err)
	}
	if reg.deleted {
		t.Error("the registry was cleared although nothing could be removed")
	}
	if !exists(home, "runtimes/53/package.json") || !exists(home, ExeName()) || !exists(home, markerName()) || !exists(home, "node/node.exe") {
		t.Error("a refused uninstall must leave the install whole")
	}
	if left, _ := filepath.Glob(filepath.Join(home, ".install-*")); len(left) != 0 {
		t.Errorf("trash folder left behind: %v", left)
	}
}

func TestRelativeHomeIsMadeAbsolute(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	self := filepath.Join(base, "setup.exe")
	write(t, base, "setup.exe", "x")
	reg := &fakeRegistry{}
	if err := install("rel-home", payloadFS("1"), self, reg); err != nil {
		t.Fatal(err)
	}
	cmd := mustFind(t, reg.written, progKey+`\shell\open\command`, "")
	want := `"` + filepath.Join(base, "rel-home", ExeName()) + `" open "%1"`
	if cmd != want {
		t.Errorf("command = %s, want %s", cmd, want)
	}
	if !exists(base, "rel-home/"+markerName()) {
		t.Error("no marker in the install folder")
	}
}

func TestCleanupCommandHoldsNoPaths(t *testing.T) {
	// The paths reach cmd.exe through the environment, so nothing in the line is parsed
	// from a path: no '%', '&' or '^' of a folder name can change what runs.
	for _, want := range []string{"%" + cleanupExeVar + "%", "%" + cleanupHomeVar + "%", "del /f /q", "rmdir", "for /l"} {
		if !strings.Contains(cleanupLine, want) {
			t.Errorf("cleanup line lacks %q: %s", want, cleanupLine)
		}
	}
	if strings.Contains(cleanupLine, `\`) || strings.Contains(strings.ToLower(cleanupLine), "users") {
		t.Errorf("cleanup line holds a path: %s", cleanupLine)
	}
}

func TestPayloadNoneWithoutTag(t *testing.T) {
	if _, ok := Payload(); ok && !payloadBuilt {
		t.Error("Payload reports a payload in a build without the tag")
	}
}

func TestUnpackKeepsLinksInsideAndProgramModes(t *testing.T) {
	home := t.TempDir()
	p := payloadFS("1")
	p["node/bin/node"] = &fstest.MapFile{Data: []byte("ELF"), Mode: 0o755}
	p["node/lib/npm-cli.js"] = &fstest.MapFile{Data: []byte("js"), Mode: 0o644}
	p["node/bin/npm"] = &fstest.MapFile{Data: []byte("../lib/npm-cli.js"), Mode: fs.ModeSymlink}
	if err := installFiles(home, p); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(home, "node/bin/node")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("node/bin/node: %v, %v; want mode 0755", fi.Mode(), err)
	}
	if fi, err := os.Stat(filepath.Join(home, "node/lib/npm-cli.js")); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("npm-cli.js: %v, %v; want mode 0644", fi.Mode(), err)
	}
	if to, err := os.Readlink(filepath.Join(home, "node/bin/npm")); err != nil || to != filepath.FromSlash("../lib/npm-cli.js") {
		t.Errorf("node/bin/npm -> %q, %v", to, err)
	}
	if got := read(t, home, "node/bin/npm"); got != "js" {
		t.Errorf("the link does not lead to its file: %q", got)
	}

	for _, to := range []string{"/etc/passwd", "../../../../x", `..\\x`} {
		bad := payloadFS("2")
		bad["node/bin/bad"] = &fstest.MapFile{Data: []byte(to), Mode: fs.ModeSymlink}
		if err := installFiles(home, bad); err == nil {
			t.Errorf("link to %q accepted", to)
		}
	}
}

func TestDirPayloadShowsOnlyTheRuntime(t *testing.T) {
	build := t.TempDir()
	for _, rel := range []string{"node/bin/node", "runtimes/53/package.json", "runtimes/53/work/abc/slides.md",
		"journal/x.json", "logs/viewer.log", "instances/a.json", ExeName()} {
		write(t, build, rel, "x")
	}
	os.Symlink("node", filepath.Join(build, "node/bin/nodejs"))
	home := filepath.Join(t.TempDir(), "home")
	p, ok := dirPayload(build, home)
	if !ok {
		t.Fatal("no payload in a build folder")
	}
	var got []string
	fs.WalkDir(p, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		if !d.IsDir() {
			got = append(got, path)
		}
		return nil
	})
	want := []string{"node/bin/node", "node/bin/nodejs", "runtimes/53/package.json"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("payload files = %q, want %q", got, want)
	}
	if _, err := fs.Stat(p, "runtimes/53/work/abc/slides.md"); err == nil {
		t.Error("a work folder can be opened through the payload")
	}
	if _, err := fs.Stat(p, ExeName()); err == nil {
		t.Error("the program is in the payload: install would not copy the running one")
	}
	if err := installFiles(home, p); err != nil {
		t.Fatal(err)
	}
	if to, err := os.Readlink(filepath.Join(home, "node/bin/nodejs")); err != nil || to != "node" {
		t.Errorf("link not installed: %q, %v", to, err)
	}

	// Run from the install folder itself: nothing to unpack.
	if p, ok := dirPayload(home, home); !ok {
		t.Error("an install folder has a runtime")
	} else if ents, _ := fs.ReadDir(p, "."); len(ents) != 0 {
		t.Errorf("payload of the install folder itself = %v, want none", ents)
	}
	if _, ok := dirPayload(t.TempDir(), home); ok {
		t.Error("payload found in an empty folder")
	}
}
