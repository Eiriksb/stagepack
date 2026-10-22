package version

import "testing"

func TestWindows(t *testing.T) {
	for in, want := range map[string]string{
		"0.1.0":       "0.1.0.0",
		"1.2.3.4":     "1.2.3.4",
		"2.0.1-rc.1":  "2.0.1.0",
		"dev":         "0.0.0.0",
		"":            "0.0.0.0",
		"1.x.3":       "1.0.0.0",
		"70000.1":     "0.0.0.0",
		"3.4+build.5": "3.4.0.0",
		"1.2.3.4.5":   "1.2.3.0",
	} {
		if got := Windows(in); got != want {
			t.Errorf("Windows(%q) = %q, want %q", in, got, want)
		}
	}
}
