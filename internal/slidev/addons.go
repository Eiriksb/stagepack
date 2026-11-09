package slidev

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"stagepack/internal/atomicfile"
)

// ExtraAddonsEnv holds the addons the preload adds to the talk's, as a JSON array of absolute
// folders with forward slashes.
const ExtraAddonsEnv = "STAGEPACK_EXTRA_ADDONS"

// addonsPreload is the Node --import preload that makes Slidev load the addons in
// ExtraAddonsEnv as if the talk's package.json listed them, without writing to the talk.
//
//go:embed addons-preload.mjs
var addonsPreload []byte

// AddonLaunch returns the Config.NodeArgs and Config.Env that make Slidev load addons, folders
// outside the talk, on top of the talk's own. It writes the embedded preload to
// <home>/lib/addons-preload-<sha256[:12]>.mjs, unless that file holds it already, and returns
// NodeArgs {"--import", <file URL of it>} and Env {ExtraAddonsEnv + "=" + JSON array of
// absolute, forward-slash paths}. With no addons it returns nil, nil, nil: no preload at all.
//
// Addons passed this way are the caller's (an editor's), not the talk's, so they never count as
// the talk's code: the trust check reads only the talk's files and headmatter.
func AddonLaunch(home string, addons []string) (nodeArgs, env []string, err error) {
	if len(addons) == 0 {
		return nil, nil, nil
	}
	paths := make([]string, len(addons))
	for i, a := range addons {
		abs, err := filepath.Abs(a)
		if err != nil {
			return nil, nil, err
		}
		// Slidev resolves an addon path with forward slashes on Windows too (C:/...).
		paths[i] = filepath.ToSlash(abs)
	}
	list, err := json.Marshal(paths)
	if err != nil {
		return nil, nil, err
	}
	lib, err := filepath.Abs(filepath.Join(home, "lib"))
	if err != nil {
		return nil, nil, err
	}
	preload, err := writePreload(lib)
	if err != nil {
		return nil, nil, err
	}
	return []string{"--import", fileURL(filepath.ToSlash(preload))}, []string{ExtraAddonsEnv + "=" + string(list)}, nil
}

// writePreload writes the embedded preload into dir unless it is there already, and returns its
// path. The name carries the content's hash, so a new preload gets a new file, and a Slidev
// still running keeps the one it loaded. A file at that name that doesn't hold exactly the
// preload (a crash cut it short, say) is replaced, so the addons are never silently missing.
func writePreload(dir string) (string, error) {
	sum := sha256.Sum256(addonsPreload)
	p := filepath.Join(dir, "addons-preload-"+hex.EncodeToString(sum[:])[:12]+".mjs")
	if holdsPreload(p) {
		return p, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".addons-preload-*.tmp")
	if err != nil {
		return "", err
	}
	_, werr := tmp.Write(addonsPreload)
	if err := errors.Join(werr, tmp.Sync(), tmp.Close()); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := atomicfile.Rename(tmp.Name(), p); err != nil {
		os.Remove(tmp.Name())
		// Another launch may have put it there meanwhile (Windows refuses to rename over a
		// file in use).
		if holdsPreload(p) {
			return p, nil
		}
		return "", err
	}
	atomicfile.SyncDir(dir)
	return p, nil
}

// holdsPreload reports whether the file at p is exactly the embedded preload.
func holdsPreload(p string) bool {
	data, err := os.ReadFile(p)
	return err == nil && bytes.Equal(data, addonsPreload)
}

// fileURL turns an absolute path with forward slashes into a file URL Node accepts:
// file:///home/u/a%20b.mjs, or file:///C:/Users/u/a.mjs on Windows.
func fileURL(slashed string) string {
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed // C:/x
	}
	return (&url.URL{Scheme: "file", Path: slashed}).String()
}
