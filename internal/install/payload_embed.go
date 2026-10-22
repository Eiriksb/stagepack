//go:build payload

package install

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"io/fs"
)

const payloadBuilt = true

// payload.zip is made by build/payload.sh. It is embedded as is and read
// through its central directory, so no entry is read before it is unpacked.
//
//go:embed payload.zip
var payloadZip []byte

// Payload returns the bundled node/ and runtimes/ folders.
func Payload() (fs.FS, bool) {
	zr, err := zip.NewReader(bytes.NewReader(payloadZip), int64(len(payloadZip)))
	if err != nil {
		return nil, false
	}
	return zr, true
}
