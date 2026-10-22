// Package version holds the release this program was built as. `make release`
// sets it with -ldflags "-X stagepack/internal/version.Version=0.1.0"; any other
// build is "dev".
package version

import (
	"strconv"
	"strings"
)

// Version is the release, without a leading "v" ("0.1.0"), or "dev".
var Version = "dev"

// Windows is Version as the four numbers of a Windows version resource
// ("0.1.0.0"). Anything that is not up to four dot-separated numbers, such as
// "dev" or a pre-release suffix, counts as 0 from there on.
func Windows(v string) string {
	if j := strings.IndexAny(v, "-+"); j >= 0 {
		v = v[:j]
	}
	n := [4]int{}
	for i, p := range strings.SplitN(v, ".", 4) {
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 || x > 65535 {
			break
		}
		n[i] = x
	}
	return strconv.Itoa(n[0]) + "." + strconv.Itoa(n[1]) + "." + strconv.Itoa(n[2]) + "." + strconv.Itoa(n[3])
}
