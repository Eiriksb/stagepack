package instance

import (
	"path"
	"strings"
)

// IsThrowawayCopy reports whether the deck at p sits somewhere that is cleaned up or
// invisible to the user: temp folders and the caches that Outlook and Teams open
// attachments from. Edits saved there are lost, so the caller warns first.
// It is pure: goos and env are passed in so both OSes can be tested anywhere.
func IsThrowawayCopy(p, goos string, env func(string) string) bool {
	if goos == "windows" {
		return throwawayWindows(p, env)
	}
	return throwawayUnix(p, env)
}

func throwawayUnix(p string, env func(string) string) bool {
	tmp := env("TMPDIR")
	if tmp == "" {
		tmp = "/tmp"
	}
	for _, dir := range []string{tmp, "/var/tmp"} {
		if under(path.Clean(p), path.Clean(dir), "/") {
			return true
		}
	}
	return false
}

func throwawayWindows(p string, env func(string) string) bool {
	p = winNorm(p)
	local, roaming := winNorm(env("LOCALAPPDATA")), winNorm(env("APPDATA"))

	var dirs []string
	for _, v := range []string{"TEMP", "TMP"} {
		dirs = append(dirs, winNorm(env(v)))
	}
	if local != "" {
		dirs = append(dirs,
			// TEMP is often the 8.3 short form, which would not match the long path.
			local+`\temp`,
			local+`\microsoft\windows\inetcache`,
			local+`\microsoft\windows\temporary internet files`,
			local+`\microsoft\olk`, // new Outlook attachments
		)
	}
	if roaming != "" {
		dirs = append(dirs, roaming+`\microsoft\teams`)
	}
	for _, d := range dirs {
		if d != "" && under(p, d, `\`) {
			return true
		}
	}
	// New Teams keeps its files in a package folder whose suffix varies.
	if local != "" {
		pkgs := local + `\packages\`
		if strings.HasPrefix(p, pkgs) {
			name, rest, _ := strings.Cut(p[len(pkgs):], `\`)
			return rest != "" && strings.HasPrefix(name, "msteams_")
		}
	}
	return false
}

// winNorm lower-cases, uses `\` and drops repeated and trailing separators. UNC prefixes stay.
func winNorm(s string) string {
	if s == "" {
		return ""
	}
	return PathKey(s, "windows")
}

// under reports whether p is strictly inside dir (not dir itself), on a separator boundary.
func under(p, dir, sep string) bool {
	dir = strings.TrimRight(dir, sep)
	return dir != "" && strings.HasPrefix(p, dir+sep)
}
