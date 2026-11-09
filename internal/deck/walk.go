package deck

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
)

// DanglingLinkError is a symlink in a project whose target is not there.
type DanglingLinkError struct{ Rel, Target string }

func (e *DanglingLinkError) Error() string {
	return fmt.Sprintf("%s is a link to %s, which is not here", e.Rel, e.Target)
}

// LoopError is a link that leads back to a folder that contains it.
type LoopError struct{ Rel string }

// LinkError is a symlink where none is allowed (see ProjectFilesNoLinks).
type LinkError struct{ Rel string }

func (e *LinkError) Error() string {
	return fmt.Sprintf("%s is a link, which a deck cannot hold", e.Rel)
}

func (e *LoopError) Error() string {
	return fmt.Sprintf("%s links back to a folder that contains it", e.Rel)
}

// WalkProject calls visit for every regular file in the project at root, in
// folder order, with its slash-separated path, its real path and its Stat
// (links are followed). It never enters node_modules or .git, nor the
// top-level dist and .slidev. skip, if not nil, is asked about every other path
// first (files and folders) and a true answer leaves it out unlooked at, so a
// link there need not lead anywhere. A link that leads nowhere is a
// *DanglingLinkError; a link that loops, a *LoopError.
func WalkProject(root string, skip func(rel string) bool, visit func(rel, abs string, st fs.FileInfo) error) error {
	return walkProject(root, false, skip, visit)
}

func walkProject(root string, noLinks bool, skip func(rel string) bool, visit func(rel, abs string, st fs.FileInfo) error) error {
	var walk func(abs, rel string, ancestors []string) error
	walk = func(abs, rel string, ancestors []string) error {
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		for _, a := range ancestors {
			if a == real {
				return &LoopError{Rel: rel}
			}
		}
		ancestors = append(ancestors, real)
		des, err := os.ReadDir(abs)
		if err != nil {
			return err
		}
		for _, de := range des {
			name := de.Name()
			childRel := path.Join(rel, name)
			if rel == "" {
				childRel = name
			}
			childAbs := filepath.Join(abs, name)
			if skip != nil && skip(childRel) {
				continue
			}
			if noLinks {
				if lst, err := os.Lstat(childAbs); err == nil && lst.Mode()&fs.ModeSymlink != 0 {
					return &LinkError{Rel: childRel}
				}
			}
			st, err := os.Stat(childAbs) // follows symlinks
			if err != nil {
				if target, dangling := danglingLink(childAbs, err); dangling {
					return &DanglingLinkError{Rel: childRel, Target: target}
				}
				return fmt.Errorf("%s: %w", childRel, err)
			}
			if st.IsDir() {
				switch {
				case name == "node_modules" || name == ".git":
					continue
				case rel == "" && (name == "dist" || name == ".slidev"):
					continue
				}
				if err := walk(childAbs, childRel, ancestors); err != nil {
					return err
				}
				continue
			}
			if !st.Mode().IsRegular() {
				continue
			}
			if err := visit(childRel, childAbs, st); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(root, "", nil)
}

// ProjectFiles lists the project's files at root as sorted slash paths, by the
// rules of WalkProject: no node_modules or .git anywhere, no top-level dist or .slidev.
func ProjectFiles(root string) ([]string, error) {
	return projectFiles(root, false)
}

// ProjectFilesNoLinks is ProjectFiles for a folder that must hold no links
// (a work folder): a link anywhere in it is a *LinkError, not followed.
func ProjectFilesNoLinks(root string) ([]string, error) {
	return projectFiles(root, true)
}

func projectFiles(root string, noLinks bool) ([]string, error) {
	var files []string
	err := walkProject(root, noLinks, nil, func(rel, _ string, _ fs.FileInfo) error {
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// danglingLink reports whether p, which failed to stat with err, is a symlink
// whose target does not exist, and that target.
func danglingLink(p string, err error) (string, bool) {
	if !errors.Is(err, fs.ErrNotExist) {
		return "", false
	}
	if lst, lerr := os.Lstat(p); lerr != nil || lst.Mode()&fs.ModeSymlink == 0 {
		return "", false
	}
	target, rerr := os.Readlink(p)
	if rerr != nil {
		return "", false
	}
	return target, true
}
