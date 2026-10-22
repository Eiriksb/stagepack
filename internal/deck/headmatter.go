package deck

import "strings"

// The first --- block of a slides file (its headmatter), read just far enough for
// the few top-level keys pack and the viewer look at. It is not a YAML parser.

// BOM is the UTF-8 byte order mark some editors put at the start of a file.
const BOM = "\uFEFF"

// isFence reports whether line is a --- fence (trailing spaces, tabs and \r allowed).
func isFence(line string) bool { return strings.TrimRight(line, " \t\r") == "---" }

// Headmatter splits text into the lines of its first --- block (without the fences).
// A leading byte order mark is ignored. ok is false if the text does not start with
// a --- line or the block is not closed.
func Headmatter(text string) (lines []string, ok bool) {
	all := strings.Split(strings.TrimPrefix(text, BOM), "\n")
	if !isFence(all[0]) {
		return nil, false
	}
	for i := 1; i < len(all); i++ {
		if isFence(all[i]) {
			return all[1:i], true
		}
	}
	return nil, false
}

// HeadmatterValue returns the value of a top-level `key: value` line in the first --- block.
func HeadmatterValue(text, key string) string {
	lines, ok := Headmatter(text)
	if !ok {
		return ""
	}
	for _, l := range lines {
		if v, found := KeyValue(l, key); found {
			return v
		}
	}
	return ""
}

// KeyValue returns the value of a `key: value` line, without quotes or a trailing # comment.
func KeyValue(line, key string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), key+":")
	if !ok {
		return "", false
	}
	return plainValue(rest), true
}

// plainValue is a YAML scalar without its quotes or a trailing # comment.
func plainValue(v string) string {
	v = strings.TrimSpace(v)
	if v != "" && (v[0] == '"' || v[0] == '\'') {
		if j := strings.IndexByte(v[1:], v[0]); j >= 0 {
			return v[1 : 1+j]
		}
		return v
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}
