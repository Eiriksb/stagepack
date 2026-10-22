// Package deck describes the contents of a deck file (brand.Ext).
package deck

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"stagepack/internal/brand"
)

// FormatVersion is the manifest format this build reads and writes.
const FormatVersion = 1

// Manifest is the brand.ManifestName file at the root of a deck file.
type Manifest struct {
	Format  int               `json:"format"`
	ID      string            `json:"id"`
	Title   string            `json:"title"`
	Created time.Time         `json:"created"`
	Updated time.Time         `json:"updated"`
	Slidev  string            `json:"slidev"` // Slidev version the deck was packed with
	Entry   string            `json:"entry"`  // slides file, e.g. "slides.md"
	Meta    map[string]string `json:"meta"`
}

// maxManifestSize bounds how much of the manifest entry is read.
const maxManifestSize = 1 << 20

// TooNewError means this viewer cannot open the deck. Usually the deck is newer
// than the viewer; with Older set the deck was made for an older Slidev major
// and has to be repacked. (The type name is kept for interface stability.)
type TooNewError struct {
	Reason string
	Older  bool
	// Slidev majors, set for a major mismatch (Older or not).
	DeckMajor, RuntimeMajor int
}

func (e *TooNewError) Error() string {
	if e.Older {
		return fmt.Sprintf("This talk was made for Slidev %d; this viewer runs Slidev %d. Ask for it to be repacked with a current %s.",
			e.DeckMajor, e.RuntimeMajor, brand.Name)
	}
	return fmt.Sprintf("this deck needs a newer %s viewer: %s", brand.Name, e.Reason)
}

// ReadManifest reads and decodes the manifest from an opened deck file.
func ReadManifest(r *zip.Reader) (Manifest, error) {
	var m Manifest
	for _, f := range r.File {
		if f.Name != brand.ManifestName {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return m, fmt.Errorf("open %s: %w", brand.ManifestName, err)
		}
		defer rc.Close()
		data, err := io.ReadAll(io.LimitReader(rc, maxManifestSize+1))
		if err != nil {
			return m, fmt.Errorf("read %s: %w", brand.ManifestName, err)
		}
		if len(data) > maxManifestSize {
			return m, fmt.Errorf("%s is too large (over %d bytes)", brand.ManifestName, maxManifestSize)
		}
		if err := json.Unmarshal(data, &m); err != nil {
			return Manifest{}, fmt.Errorf("parse %s: %w", brand.ManifestName, err)
		}
		return m, nil
	}
	return m, fmt.Errorf("not a %s file: %s is missing", brand.Name, brand.ManifestName)
}

// Check decides whether this viewer can open the deck. It refuses a newer
// manifest format and a Slidev major version different from runtimeSlidev;
// a minor or patch difference is accepted.
func Check(m Manifest, runtimeSlidev string) error {
	if m.Format > FormatVersion {
		return &TooNewError{Reason: fmt.Sprintf("deck format %d, this viewer reads up to %d", m.Format, FormatVersion)}
	}
	deckMajor, err := Major(m.Slidev)
	if err != nil {
		return fmt.Errorf("deck Slidev version: %w", err)
	}
	runMajor, err := Major(runtimeSlidev)
	if err != nil {
		return fmt.Errorf("runtime Slidev version: %w", err)
	}
	if deckMajor != runMajor {
		return &TooNewError{
			Reason:       fmt.Sprintf("deck uses Slidev %s, this viewer bundles Slidev %s", m.Slidev, runtimeSlidev),
			Older:        deckMajor < runMajor,
			DeckMajor:    deckMajor,
			RuntimeMajor: runMajor,
		}
	}
	return nil
}

// Major returns the leading integer of a version such as "53.0.0" or "^53.0.0".
func Major(v string) (int, error) {
	s := strings.TrimLeft(strings.TrimSpace(v), "^~=v><! ")
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, fmt.Errorf("no major version in %q", v)
	}
	return strconv.Atoi(s[:end])
}
