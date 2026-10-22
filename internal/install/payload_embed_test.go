//go:build payload

package install

import (
	"io/fs"
	"path/filepath"
	"strconv"
	"testing"

	slidevrt "stagepack/runtime"
)

// Runs only with -tags payload (after `make payload`): the embedded payload holds what the
// viewer needs, and unpacks. The unpack writes about 600 MB, so -short skips it.
func TestEmbeddedPayload(t *testing.T) {
	p, ok := Payload()
	if !ok {
		t.Fatal("no payload in a payload build")
	}
	rt := "runtimes/" + strconv.Itoa(slidevrt.Major())
	for _, name := range []string{"node/node.exe", ExeName(), rt + "/package.json", rt + "/node_modules/@slidev/cli/package.json"} {
		if _, err := fs.Stat(p, name); err != nil {
			t.Errorf("payload lacks %s: %v", name, err)
		}
	}
	if testing.Short() {
		t.Skip("-short: not unpacking")
	}
	home := t.TempDir()
	if err := installFiles(home, p); err != nil {
		t.Fatal(err)
	}
	var want, got int
	fs.WalkDir(p, ".", func(_ string, d fs.DirEntry, _ error) error {
		if !d.IsDir() {
			want++
		}
		return nil
	})
	filepath.WalkDir(home, func(_ string, d fs.DirEntry, _ error) error {
		if !d.IsDir() {
			got++
		}
		return nil
	})
	if got != want {
		t.Errorf("unpacked %d files, payload has %d", got, want)
	}
}
