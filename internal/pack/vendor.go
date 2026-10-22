package pack

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"stagepack/internal/brand"
	"stagepack/runtime"
)

type packageJSON struct {
	Name                 string            `json:"name"`
	Version              string            `json:"version"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
	PeerDependenciesMeta map[string]struct {
		Optional bool `json:"optional"`
	} `json:"peerDependenciesMeta"`
	Scripts map[string]string `json:"scripts"`
	OS      json.RawMessage   `json:"os"`
	CPU     json.RawMessage   `json:"cpu"`
}

func readPackageJSON(p string) (packageJSON, error) {
	var pj packageJSON
	data, err := os.ReadFile(p)
	if err != nil {
		return pj, err
	}
	if err := json.Unmarshal(data, &pj); err != nil {
		return pj, fmt.Errorf("%s: %w", p, err)
	}
	return pj, nil
}

// requiredPeers are the peerDependencies not marked optional.
func (pj packageJSON) requiredPeers() []string {
	var names []string
	for n := range pj.PeerDependencies {
		if !pj.PeerDependenciesMeta[n].Optional {
			names = append(names, n)
		}
	}
	return names
}

// vpkg is one package to vendor. rel is its place under vendor/node_modules.
type vpkg struct {
	name, dir, rel string
	parent         *vpkg
	pj             packageJSON
}

// vendor returns the zip items for every package the project needs that the runtime lacks: the closure over
// the project's dependencies, devDependencies and required peerDependencies, looked up in dir/node_modules.
func vendor(dir string, root packageJSON) ([]item, error) {
	lock := runtime.Lock()
	skip := func(name string) bool { _, inRuntime := lock[name]; return inRuntime || skipPackages[name] }

	type want struct {
		name   string
		from   *vpkg
		isPeer bool
	}
	var queue []want
	add := func(from *vpkg, pj packageJSON) {
		var names []string
		for _, m := range []map[string]string{pj.Dependencies, pj.DevDependencies} {
			for n := range m {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		for _, n := range names {
			queue = append(queue, want{name: n, from: from})
		}
		peers := pj.requiredPeers()
		sort.Strings(peers)
		for _, n := range peers {
			queue = append(queue, want{name: n, from: from, isPeer: true})
		}
	}
	// devDependencies only count for the project itself, not for vendored packages.
	rootPJ := root
	add(nil, rootPJ)

	seen := map[string]bool{}
	var pkgs []*vpkg
	for len(queue) > 0 {
		w := queue[0]
		queue = queue[1:]
		if skip(w.name) {
			continue
		}
		p, found, err := resolve(dir, w.from, w.name)
		if err != nil {
			return nil, err
		}
		if !found {
			if w.isPeer {
				continue // a missing peer is the project's concern, not a reason to refuse
			}
			by := "package.json"
			if w.from != nil {
				by = w.from.name
			}
			return nil, fmt.Errorf("pack: %s needs %s, which is not installed in node_modules (run npm install)", by, w.name)
		}
		if seen[p.rel] {
			continue
		}
		seen[p.rel] = true
		pkgs = append(pkgs, p)
		sub := p.pj
		sub.DevDependencies = nil
		add(p, sub)
	}

	var items []item
	for _, p := range pkgs {
		files, err := packageFiles(p)
		if err != nil {
			return nil, err
		}
		items = append(items, files...)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].name < items[j].name })
	return items, nil
}

// resolve finds name the way Node does: in nested node_modules of from and its ancestors, then the top.
func resolve(dir string, from *vpkg, name string) (*vpkg, bool, error) {
	try := func(base, relBase string) (*vpkg, bool, error) {
		d := filepath.Join(base, "node_modules", filepath.FromSlash(name))
		pj, err := readPackageJSON(filepath.Join(d, "package.json"))
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("pack: package %s: %w", name, err)
		}
		return &vpkg{name: name, dir: d, rel: path.Join(relBase, "node_modules", name), parent: from, pj: pj}, true, nil
	}
	for p := from; p != nil; p = p.parent {
		if v, ok, err := try(p.dir, p.rel); err != nil || ok {
			return v, ok, err
		}
	}
	v, ok, err := try(dir, "")
	if ok {
		v.rel = name // top-level: vendor/node_modules/<name>
		v.parent = nil
	}
	return v, ok, err
}

// packageFiles lists a package's files (not its nested node_modules) and refuses native packages.
func packageFiles(p *vpkg) ([]item, error) {
	native := func(why string) error {
		return fmt.Errorf("pack: package %s is native (%s); %s can only carry pure JS/CSS/asset packages", p.name, why, brand.Name)
	}
	if len(p.pj.OS) > 0 && string(p.pj.OS) != "null" {
		return nil, native("it sets os")
	}
	if len(p.pj.CPU) > 0 && string(p.pj.CPU) != "null" {
		return nil, native("it sets cpu")
	}
	for _, s := range []string{"install", "postinstall"} {
		if p.pj.Scripts[s] != "" {
			return nil, native("it has an " + s + " script")
		}
	}
	var items []item
	var walk func(abs, rel string, ancestors []string) error
	walk = func(abs, rel string, ancestors []string) error {
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return err
		}
		for _, a := range ancestors {
			if a == real {
				return nil // a link back to a parent: nothing new to add
			}
		}
		ancestors = append(ancestors, real)
		des, err := os.ReadDir(abs)
		if err != nil {
			return err
		}
		for _, de := range des {
			n := de.Name()
			childAbs := filepath.Join(abs, n)
			childRel := path.Join(rel, n)
			st, err := os.Stat(childAbs)
			if err != nil {
				return fmt.Errorf("pack: %s/%s: %w", p.name, childRel, err)
			}
			if st.IsDir() {
				if n == "node_modules" {
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
			if strings.HasSuffix(strings.ToLower(n), ".node") {
				return native("it contains " + childRel)
			}
			if n == "binding.gyp" {
				return native("it contains binding.gyp")
			}
			items = append(items, item{name: path.Join("vendor/node_modules", p.rel, childRel), src: childAbs, mod: st.ModTime()})
		}
		return nil
	}
	if err := walk(p.dir, "", nil); err != nil {
		return nil, err
	}
	return items, nil
}
