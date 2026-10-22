//go:build !windows

package browser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenHonoursBROWSER(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "args")
	script := filepath.Join(dir, "b.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s|' \"$@\" > \""+out+"\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BROWSER", script)
	const u = "http://127.0.0.1:1234/presenter/?a=1&b=2"
	if err := Open(u); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := os.ReadFile(out)
		if got := string(data); strings.HasSuffix(got, "|") {
			if got != u+"|" {
				t.Fatalf("BROWSER got %q, want the URL as its one argument", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("BROWSER was not run")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
