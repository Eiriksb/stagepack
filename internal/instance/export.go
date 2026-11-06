package instance

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ExportLockStale is the age at which an export's lock counts as left behind
// even though a process with its PID runs: that PID has been reused.
const ExportLockStale = 6 * time.Hour

// ExportLock is the content of instances/<key>.export, which an export to
// PowerPoint holds while it runs: its PID on the first line and, once it has
// unpacked the talk, its work folder on the second.
type ExportLock struct {
	PID     int
	WorkDir string
}

// Format is the text the lock file holds.
func (l ExportLock) Format() string {
	if l.WorkDir == "" {
		return strconv.Itoa(l.PID)
	}
	return strconv.Itoa(l.PID) + "\n" + l.WorkDir
}

// ParseExportLock reads what Format wrote; ok is false when there is no PID.
func ParseExportLock(data string) (l ExportLock, ok bool) {
	first, rest, _ := strings.Cut(data, "\n")
	pid, err := strconv.Atoi(strings.TrimSpace(first))
	if err != nil || pid <= 0 {
		return ExportLock{}, false
	}
	return ExportLock{PID: pid, WorkDir: strings.TrimSpace(rest)}, true
}

// RunningExports lists the export locks under home whose process runs and
// which are not older than ExportLockStale.
func RunningExports(home string) []ExportLock {
	paths, _ := filepath.Glob(filepath.Join(home, "instances", "*.export"))
	var out []ExportLock
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil || time.Since(fi.ModTime()) > ExportLockStale {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if l, ok := ParseExportLock(string(data)); ok && PIDRunning(l.PID) {
			out = append(out, l)
		}
	}
	return out
}
