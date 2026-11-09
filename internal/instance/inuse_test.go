package instance

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"stagepack/internal/brand"
)

// InUse keeps a young folder, the work folder of a live instance and that of a running export,
// and lets go of the rest.
func TestInUse(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	now := time.Now()
	old := now.Add(-11 * time.Minute)
	folder := func(name string, young bool) string {
		d := filepath.Join(work, name)
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if !young {
			if err := os.Chtimes(d, old, old); err != nil {
				t.Fatal(err)
			}
		}
		return d
	}
	young, idle := folder("young", true), folder("idle", false)
	live, dead := folder("live", false), folder("dead", false)
	exporting := folder("exporting", false)

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	_, h, err := Claim(home, filepath.Join(t.TempDir(), "live"+brand.Ext), neverAlive)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Update(port, srv.URL, live); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(t.TempDir(), "dead"+brand.Ext)
	if _, _, err := Claim(home, gone, neverAlive); err != nil {
		t.Fatal(err)
	}
	rec := mustRead(t, home, gone)
	rec.PID, rec.WorkDir = deadPID(t), dead
	if err := writeRecord(recordPath(home, gone), rec); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "instances", "abc.export"), []byte(ExportLock{PID: os.Getpid(), WorkDir: exporting}.Format()), 0o644); err != nil {
		t.Fatal(err)
	}

	inUse := InUse(home, now)
	for dir, want := range map[string]bool{young: true, idle: false, live: true, dead: false, exporting: true} {
		if got := inUse(dir); got != want {
			t.Errorf("InUse(%s) = %v, want %v", filepath.Base(dir), got, want)
		}
	}
	// The young folder is old an hour on.
	if InUse(home, now.Add(time.Hour))(young) {
		t.Error("InUse an hour later keeps the folder that was young")
	}
}

// After Slidev dies, serve keeps running so the person can still save: its record's PID runs
// but nothing answers at its address. The work folder stays while that PID runs.
func TestInUseKeepsTheFolderOfARunningPIDWhoseServerDoesNotAnswer(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	now := time.Now()
	old := now.Add(-11 * time.Minute)
	folder := func(name string) string {
		d := filepath.Join(work, name)
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(d, old, old); err != nil {
			t.Fatal(err)
		}
		return d
	}
	silent, dead := folder("silent"), folder("dead")

	// An address nothing listens on: a server that was closed.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	base := srv.URL
	srv.Close()

	for name, c := range map[string]struct {
		pid  int
		dir  string
		want bool
	}{
		"running": {os.Getpid(), silent, true},
		"dead":    {deadPID(t), dead, false},
	} {
		talk := filepath.Join(t.TempDir(), name+brand.Ext)
		if _, _, err := Claim(home, talk, neverAlive); err != nil {
			t.Fatal(err)
		}
		rec := mustRead(t, home, talk)
		rec.PID, rec.Port, rec.BaseURL, rec.WorkDir = c.pid, port, base, c.dir
		if err := writeRecord(recordPath(home, talk), rec); err != nil {
			t.Fatal(err)
		}
	}
	inUse := InUse(home, now)
	if !inUse(silent) {
		t.Error("InUse drops the folder of a running PID whose server does not answer")
	}
	if inUse(dead) {
		t.Error("InUse keeps the folder of a dead PID")
	}
}
