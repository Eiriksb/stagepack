package atomicfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceRemovesTempOnError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	err := Replace(p, func(w io.Writer) error {
		w.Write([]byte("partial"))
		return boom
	})
	if err != boom {
		t.Fatalf("err = %v, want boom", err)
	}
	if got, _ := os.ReadFile(p); string(got) != "old" {
		t.Errorf("file = %q, want old", got)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Errorf("folder has %d entries, want only the file", len(ents))
	}
}

func TestReplaceWritesAndKeepsMode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := Replace(p, func(w io.Writer) error { _, err := w.Write([]byte("new")); return err }); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); string(got) != "new" {
		t.Errorf("file = %q", got)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v", st.Mode().Perm())
	}
}

func TestRenameRetries(t *testing.T) {
	oldRename, oldRetry, oldWait := rename, retryRename, renameWait
	t.Cleanup(func() { rename, retryRename, renameWait = oldRename, oldRetry, oldWait })
	retryRename, renameWait = true, 0

	calls := 0
	rename = func(a, b string) error {
		calls++
		if calls < 3 {
			return errors.New("sharing violation")
		}
		return nil
	}
	if err := Rename("a", "b"); err != nil || calls != 3 {
		t.Errorf("Rename = %v after %d calls, want nil after 3", err, calls)
	}

	calls = 0
	rename = func(a, b string) error { calls++; return errors.New("sharing violation") }
	if err := Rename("a", "b"); err == nil || calls != renameAttempts {
		t.Errorf("Rename = %v after %d calls, want an error after %d", err, calls, renameAttempts)
	}

	retryRename = false
	calls = 0
	if err := Rename("a", "b"); err == nil || calls != 1 {
		t.Errorf("without retry: %v after %d calls, want an error after 1", err, calls)
	}
}
