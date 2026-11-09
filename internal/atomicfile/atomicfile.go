// Package atomicfile replaces a file so that a crash leaves the old content or
// the new, never a mix.
package atomicfile

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Replace writes the new content of path through write into a unique hidden
// temp file next to it (.<base>.<random>.tmp), gives it path's permissions,
// syncs it and renames it over path (retried on Windows), then syncs the
// folder. path must exist. On any error the temp file is removed and path is
// untouched; an error from write is returned as it is.
func Replace(path string, write func(w io.Writer) error) (err error) {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(tmp)
		}
	}()
	// Keep the file's permissions (CreateTemp makes 0600); Windows has only the read-only bit.
	if err = f.Chmod(st.Mode().Perm()); err != nil && runtime.GOOS != "windows" {
		return err
	}
	if err = write(f); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = Rename(tmp, path); err != nil {
		return err
	}
	SyncDir(filepath.Dir(path))
	return nil
}

// Renaming over a file on Windows fails for a moment while a virus scanner or
// the indexer has it open, so the rename is retried there.
var (
	rename         = os.Rename
	retryRename    = runtime.GOOS == "windows"
	renameAttempts = 5
	renameWait     = 250 * time.Millisecond // between attempts: about 1 s in all
)

// Rename renames src over dst, retrying on Windows.
func Rename(src, dst string) error {
	var err error
	for i := 0; i < renameAttempts; i++ {
		if i > 0 {
			time.Sleep(renameWait)
		}
		if err = rename(src, dst); err == nil || !retryRename {
			return err
		}
	}
	return err
}

// SyncDir makes a rename in dir durable where the OS supports it (not Windows).
func SyncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}
