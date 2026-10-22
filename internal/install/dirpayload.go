package install

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// payloadTop are the folders of a build that make up the runtime.
var payloadTop = []string{"node", "runtimes"}

// ProgramDirPayload is the runtime beside the running program, for a build that
// does not carry it inside (the Linux one: dist/linux holds the program, node/
// and runtimes/). It is false when node/ or runtimes/ is missing there. When
// the program already runs from home, the files are in place: the payload is
// empty then, and an install only registers the file type again.
func ProgramDirPayload(home string) (fs.FS, bool) {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return nil, false
	}
	return dirPayload(filepath.Dir(exe), home)
}

func dirPayload(dir, home string) (fs.FS, bool) {
	for _, n := range payloadTop {
		if !dirExists(filepath.Join(dir, n)) {
			return nil, false
		}
	}
	if a, err := os.Stat(dir); err == nil {
		if b, err := os.Stat(home); err == nil && os.SameFile(a, b) {
			return runtimeFS{os.DirFS(dir), nil}, true
		}
	}
	return runtimeFS{os.DirFS(dir), payloadTop}, true
}

// runtimeFS shows only the top folders top of a build's folder, and in them no
// runtimes/<major>/work (the talks being shown) and no other program's files.
type runtimeFS struct {
	fsys fs.FS
	top  []string
}

func (r runtimeFS) hidden(name string) bool {
	if name == "." {
		return false
	}
	parts := strings.Split(name, "/")
	shown := false
	for _, t := range r.top {
		shown = shown || parts[0] == t
	}
	return !shown || (parts[0] == "runtimes" && len(parts) >= 3 && parts[2] == "work")
}

func (r runtimeFS) check(op, name string) error {
	if !fs.ValidPath(name) {
		return &fs.PathError{Op: op, Path: name, Err: fs.ErrInvalid}
	}
	if r.hidden(name) {
		return &fs.PathError{Op: op, Path: name, Err: fs.ErrNotExist}
	}
	return nil
}

func (r runtimeFS) Open(name string) (fs.File, error) {
	if err := r.check("open", name); err != nil {
		return nil, err
	}
	return r.fsys.Open(name)
}

func (r runtimeFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if err := r.check("readdir", name); err != nil {
		return nil, err
	}
	ents, err := fs.ReadDir(r.fsys, name)
	if err != nil {
		return nil, err
	}
	out := ents[:0]
	for _, e := range ents {
		p := e.Name()
		if name != "." {
			p = name + "/" + p
		}
		if !r.hidden(p) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (r runtimeFS) Stat(name string) (fs.FileInfo, error) {
	if err := r.check("stat", name); err != nil {
		return nil, err
	}
	return fs.Stat(r.fsys, name)
}

func (r runtimeFS) ReadLink(name string) (string, error) {
	if err := r.check("readlink", name); err != nil {
		return "", err
	}
	return fs.ReadLink(r.fsys, name)
}

func (r runtimeFS) Lstat(name string) (fs.FileInfo, error) {
	if err := r.check("lstat", name); err != nil {
		return nil, err
	}
	return fs.Lstat(r.fsys, name)
}
