//go:build !payload

package install

import "io/fs"

const payloadBuilt = false

// Payload returns false: this build has no bundled runtime (build with -tags payload).
func Payload() (fs.FS, bool) { return nil, false }
