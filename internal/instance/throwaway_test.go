package instance

import (
	"testing"

	"stagepack/internal/brand"
)

func winEnv(k string) string {
	return map[string]string{
		"TEMP":         `C:\Users\a\AppData\Local\Temp`,
		"TMP":          `C:\Users\a\AppData\Local\Temp\`, // trailing separator
		"LOCALAPPDATA": `C:\Users\a\AppData\Local`,
		"APPDATA":      `C:\Users\a\AppData\Roaming`,
	}[k]
}

func TestThrowawayWindows(t *testing.T) {
	for _, p := range []string{
		`C:\Users\a\AppData\Local\Temp\x` + brand.Ext,
		`c:\users\A\appdata\local\TEMP\x` + brand.Ext,
		`C:/Users/a/AppData/Local/Temp/x` + brand.Ext,
		`C:\Users\a\AppData\Local\Temp\Sub\Deeper\x` + brand.Ext,
		`C:\Users\a\AppData\Local\Microsoft\Windows\INetCache\Content.Outlook\AB\x` + brand.Ext,
		`C:\Users\a\AppData\Local\Microsoft\Windows\Temporary Internet Files\Content.Outlook\AB\x` + brand.Ext,
		`C:\Users\a\AppData\Local\Packages\MSTeams_8wekyb3d8bbwe\LocalCache\x` + brand.Ext,
		`C:\Users\a\AppData\Roaming\Microsoft\Teams\x` + brand.Ext,
		`C:\Users\a\AppData\Roaming\Microsoft\Teams\Cache\x` + brand.Ext,
		`C:\Users\a\AppData\Local\Microsoft\Olk\Attachments\x` + brand.Ext,
	} {
		if !IsThrowawayCopy(p, "windows", winEnv) {
			t.Errorf("%s should be a throwaway copy", p)
		}
	}
}

func TestThrowawayFalse(t *testing.T) {
	for _, p := range []string{
		`C:\Users\a\Downloads\x` + brand.Ext,
		`D:\talks\x` + brand.Ext,
		`\\server\share\x` + brand.Ext,
		`D:\Templates\x` + brand.Ext,
		`C:\Users\a\AppData\Local\Temporary\x` + brand.Ext,
		`C:\Users\a\AppData\Local\TempFiles\x` + brand.Ext,
		`C:\Users\a\AppData\Local\Packages\Other_abc\x` + brand.Ext,
		`C:\Users\a\AppData\Roaming\Microsoft\TeamsOld\x` + brand.Ext,
		`C:\Users\a\Documents\Temp\x` + brand.Ext,
		`C:\Users\a\AppData\Local\Temp`, // the folder itself is not a deck inside it
	} {
		if IsThrowawayCopy(p, "windows", winEnv) {
			t.Errorf("%s should not be a throwaway copy", p)
		}
	}
	empty := func(string) string { return "" }
	if IsThrowawayCopy(`C:\Users\a\AppData\Local\Microsoft\Olk\x`+brand.Ext, "windows", empty) {
		t.Error("unset env must not match anything (and must not panic)")
	}
	if IsThrowawayCopy(`C:\x`+brand.Ext, "windows", empty) {
		t.Error("unset env: C:\\x must not match an empty prefix")
	}
}

func TestThrowawayLinux(t *testing.T) {
	env := func(string) string { return "" }
	for _, p := range []string{"/tmp/x" + brand.Ext, "/tmp/sub/x" + brand.Ext, "/var/tmp/x" + brand.Ext} {
		if !IsThrowawayCopy(p, "linux", env) {
			t.Errorf("%s should be a throwaway copy", p)
		}
	}
	for _, p := range []string{"/home/a/x" + brand.Ext, "/tmpfoo/x" + brand.Ext, "/data/tmp/x" + brand.Ext} {
		if IsThrowawayCopy(p, "linux", env) {
			t.Errorf("%s should not be a throwaway copy", p)
		}
	}
	tmpdir := func(k string) string {
		if k == "TMPDIR" {
			return "/run/user/1000/tmp/"
		}
		return ""
	}
	if !IsThrowawayCopy("/run/user/1000/tmp/x"+brand.Ext, "linux", tmpdir) {
		t.Error("TMPDIR should count")
	}
}
