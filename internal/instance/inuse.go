package instance

import (
	"os"
	"runtime"
	"time"
)

// staleWorkAge: a work folder younger than this may belong to an instance still starting, which
// has not recorded it yet.
const staleWorkAge = 10 * time.Minute

// InUse tells workspace.CleanStale which work folders to keep: those younger than staleWorkAge
// at now (an instance may be starting in it), those of the instances under home whose process
// runs (PIDRunning, not Alive: after Slidev dies, serve keeps running so the person can still
// save, and nothing answers over HTTP; a reused PID only delays a clean-up) and those of its
// running exports. The records are read once, on the first folder that is
// not young.
func InUse(home string, now time.Time) func(dir string) bool {
	var live []string
	loaded := false
	return func(dir string) bool {
		if fi, err := os.Stat(dir); err == nil && now.Sub(fi.ModTime()) < staleWorkAge {
			return true
		}
		if !loaded {
			loaded = true
			for _, r := range List(home) {
				if r.WorkDir != "" && r.PID > 0 && PIDRunning(r.PID) {
					live = append(live, r.WorkDir)
				}
			}
			for _, e := range RunningExports(home) {
				if e.WorkDir != "" {
					live = append(live, e.WorkDir)
				}
			}
		}
		for _, w := range live {
			if PathKey(w, runtime.GOOS) == PathKey(dir, runtime.GOOS) {
				return true
			}
		}
		return false
	}
}
